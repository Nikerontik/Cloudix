package mobile

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// A page loaded from file:// is not a "potentially trustworthy origin", and
// getUserMedia is refused there — which would have taken out camera and
// microphone, and with them every call, silently. 127.0.0.1 *is* trustworthy by
// definition, so the UI is served over loopback instead of loaded off disk.
//
// The listener binds to 127.0.0.1 on a kernel-chosen port: nothing outside the
// device can reach it.
var (
	assetMu  sync.Mutex
	assetLn  net.Listener
	assetSrv *http.Server
)

// StartAssets serves dir over loopback and returns the URL to load. Calling it
// twice without StopAssets returns the existing URL.
func StartAssets(dir string) (string, error) {
	assetMu.Lock()
	defer assetMu.Unlock()

	if assetLn != nil {
		return "http://" + assetLn.Addr().String() + "/", nil
	}
	if dir == "" {
		return "", errors.New("asset dir is required")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir(dir))
	mux.Handle("/", noStore(fs))

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	assetLn, assetSrv = ln, srv
	return "http://" + ln.Addr().String() + "/", nil
}

// StopAssets shuts the loopback server down. Safe to call when not running.
func StopAssets() {
	assetMu.Lock()
	srv, ln := assetSrv, assetLn
	assetSrv, assetLn = nil, nil
	assetMu.Unlock()

	if srv != nil {
		_ = srv.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// noStore keeps the WebView from caching a stale bundle across app updates,
// where the assets change but the URL does not.
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}
