package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// desktopHost implements app.Host on top of the Wails runtime. Keeping it here
// rather than in backend/app is what lets the whole backend cross-compile to
// android/* and ios/arm64 — Wails has no mobile target.
type desktopHost struct{ ctx context.Context }

// AttachContext receives the Wails context from App.OnStartup, satisfying
// app.ContextAware. Every method no-ops until then, which is the same guard the
// old a.ctx == nil checks provided.
func (h *desktopHost) AttachContext(ctx context.Context) { h.ctx = ctx }

func (h *desktopHost) Emit(name string, payload interface{}) {
	if h.ctx == nil {
		return
	}
	runtime.EventsEmit(h.ctx, name, payload)
}

func (h *desktopHost) Logf(level, format string, args ...interface{}) {
	if h.ctx == nil {
		return
	}
	switch level {
	case "warn":
		runtime.LogWarningf(h.ctx, format, args...)
	case "info":
		runtime.LogInfof(h.ctx, format, args...)
	default:
		runtime.LogErrorf(h.ctx, format, args...)
	}
}

// SaveMedia asks for a path and writes the bytes. WKWebView ignores the HTML
// download attribute for data: URLs, so the in-chat download button did nothing
// on macOS; a native dialog works on both desktop platforms.
func (h *desktopHost) SaveMedia(suggestedName string, data []byte) (string, error) {
	if h.ctx == nil {
		return "", fmt.Errorf("app not started")
	}
	path, err := runtime.SaveFileDialog(h.ctx, runtime.SaveDialogOptions{
		DefaultFilename:      suggestedName,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // cancelled
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func (h *desktopHost) OpenFolder(dir string) error {
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", dir)
	case "windows":
		cmd = exec.Command("explorer.exe", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	// explorer.exe exits with a non-zero status even when it succeeds.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
