// Package mobile is the gomobile-bind facade around backend/app.
//
// gobind accepts only a narrow subset of Go types — signed ints, string, bool,
// []byte, and interfaces built from those. Slices of structs, which is what
// most of *App returns, are dropped *silently*: no error, no warning, just a
// missing method. So nothing from backend/app is exposed directly. Everything
// goes through one string-in/string-out entry point, and Call dispatches by
// reflection so this file cannot drift out of sync with app.go.
//
// Arguments arrive as a JSON array of positional values, matching how the Wails
// bindings call the same methods from JavaScript. That is what lets the
// existing frontend run unchanged on top of this.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"cloudix/backend/app"
	"cloudix/backend/storage"
)

// Callback is implemented on the Swift/Kotlin side.
type Callback interface {
	// OnEvent replaces runtime.EventsEmit: same event names, same JSON payloads.
	OnEvent(name string, payloadJSON string)
	// OnLog carries diagnostics at "error", "warn" or "info".
	OnLog(level string, message string)
	// SaveFile hands decoded bytes to the platform — a share sheet on iOS, the
	// Downloads collection on Android — and reports where they landed, or ""
	// if the user cancelled.
	SaveFile(suggestedName string, data []byte) (string, error)
}

// lifecycle methods are driven by the native shell, not by the UI, so Call must
// not be able to reach them.
var notCallable = map[string]bool{
	"OnStartup":     true,
	"OnBeforeClose": true,
	"SetHost":       true,
}

var (
	mu      sync.RWMutex
	core    *app.App
	cb      Callback
	feature Features
)

// Features tells the UI what this build can actually do, so it can hide what it
// cannot offer instead of showing a control that does nothing. The native shell
// decides the values: they depend on the platform and, on iOS, on which
// entitlements the signing account actually carries.
type Features struct {
	Calls              bool `json:"calls"`
	ScreenShareSend    bool `json:"screenShareSend"`
	ScreenShareReceive bool `json:"screenShareReceive"`
	LANDiscovery       bool `json:"lanDiscovery"`
	ManualPeers        bool `json:"manualPeers"`
	NetworkHosting     bool `json:"networkHosting"`
	BackgroundDelivery bool `json:"backgroundDelivery"`
	OpenDataFolder     bool `json:"openDataFolder"`
	Notifications      bool `json:"notifications"`
}

// Start boots the core. dataDir is supplied by the host because
// os.UserConfigDir() is meaningless inside an app sandbox; featuresJSON is a
// Features object.
func Start(dataDir string, featuresJSON string, callback Callback) error {
	mu.Lock()
	defer mu.Unlock()
	if core != nil {
		return errors.New("already started")
	}
	if dataDir == "" {
		return errors.New("dataDir is required")
	}
	if callback == nil {
		return errors.New("callback is required")
	}

	var f Features
	if featuresJSON != "" {
		if err := json.Unmarshal([]byte(featuresJSON), &f); err != nil {
			return fmt.Errorf("features: %w", err)
		}
	}
	feature = f
	cb = callback

	storage.SetDataDir(dataDir)

	a := app.NewApp(bridgeHost{})
	a.OnStartup(context.Background())
	core = a
	return nil
}

// Stop shuts the core down. Safe to call more than once.
func Stop() {
	mu.Lock()
	a := core
	core = nil
	mu.Unlock()

	if a != nil {
		a.OnBeforeClose(context.Background())
	}

	mu.Lock()
	cb = nil
	mu.Unlock()
}

// Started reports whether the core is running, so the shell can avoid a
// double Start after a configuration change recreates the Activity.
func Started() bool {
	mu.RLock()
	defer mu.RUnlock()
	return core != nil
}

// FeaturesJSON returns what this build supports, for the UI to gate on.
func FeaturesJSON() string {
	mu.RLock()
	f := feature
	mu.RUnlock()
	b, _ := json.Marshal(f)
	return string(b)
}

// Call invokes a bound method of *App by name. argsJSON is a JSON array of
// positional arguments; the result is the method's return value as JSON, or ""
// for a method that only returns an error.
func Call(method string, argsJSON string) (string, error) {
	mu.RLock()
	a := core
	mu.RUnlock()
	if a == nil {
		return "", errors.New("not started")
	}
	if notCallable[method] {
		return "", fmt.Errorf("method %q is not callable from the UI", method)
	}

	m := reflect.ValueOf(a).MethodByName(method)
	if !m.IsValid() {
		return "", fmt.Errorf("unknown method %q", method)
	}
	mt := m.Type()

	var raw []json.RawMessage
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
			return "", fmt.Errorf("args must be a JSON array: %w", err)
		}
	}
	if len(raw) != mt.NumIn() {
		return "", fmt.Errorf("%s expects %d argument(s), got %d", method, mt.NumIn(), len(raw))
	}

	in := make([]reflect.Value, mt.NumIn())
	for i := range in {
		pv := reflect.New(mt.In(i))
		if err := json.Unmarshal(raw[i], pv.Interface()); err != nil {
			return "", fmt.Errorf("%s argument %d: %w", method, i+1, err)
		}
		in[i] = pv.Elem()
	}

	return encodeResults(m.Call(in))
}

// encodeResults mirrors the Wails convention: a trailing error becomes a thrown
// exception, anything before it becomes the resolved value.
func encodeResults(out []reflect.Value) (string, error) {
	var value interface{}
	for _, v := range out {
		if v.Type() == reflect.TypeOf((*error)(nil)).Elem() {
			if !v.IsNil() {
				return "", v.Interface().(error)
			}
			continue
		}
		value = v.Interface()
	}
	if value == nil {
		return "", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}

// bridgeHost implements app.Host by forwarding to the native callback.
type bridgeHost struct{}

func currentCallback() Callback {
	mu.RLock()
	defer mu.RUnlock()
	return cb
}

func (bridgeHost) Emit(name string, payload interface{}) {
	c := currentCallback()
	if c == nil {
		return
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	c.OnEvent(name, string(b))
}

func (bridgeHost) Logf(level, format string, args ...interface{}) {
	c := currentCallback()
	if c == nil {
		return
	}
	c.OnLog(level, fmt.Sprintf(format, args...))
}

func (bridgeHost) SaveMedia(suggestedName string, data []byte) (string, error) {
	c := currentCallback()
	if c == nil {
		return "", errors.New("no callback attached")
	}
	return c.SaveFile(suggestedName, data)
}

func (bridgeHost) OpenFolder(string) error {
	return errors.New("no file manager on this platform")
}
