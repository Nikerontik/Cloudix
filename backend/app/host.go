package app

import (
	"context"
	"errors"
)

var errNoHost = errors.New("no platform host attached")

// Host is everything App needs from the platform it runs on: a channel to the
// UI, a log sink, and the two file operations that have no portable form.
// Wails supplies one on desktop, the WebView bridge supplies another on
// mobile, so backend/app itself pulls in no platform SDK and cross-compiles to
// android/* and ios/arm64 untouched.
type Host interface {
	// Emit delivers an event to the UI. Names match the desktop set:
	// peers:update, message:incoming, vpn:status and so on.
	Emit(name string, payload interface{})

	// Logf records a diagnostic at "error", "warn" or "info".
	Logf(level, format string, args ...interface{})

	// SaveMedia hands already-decoded bytes to the user and reports where they
	// landed, or "" if the save was cancelled.
	SaveMedia(suggestedName string, data []byte) (string, error)

	// OpenFolder reveals a directory in the platform's file manager. Mobile
	// has no such concept and returns an error.
	OpenFolder(dir string) error
}

// ContextAware is implemented by a Host that needs the Wails context — which
// only exists once OnStartup runs. It is an optional interface so the mobile
// host, which has no such concept, does not have to fake one.
type ContextAware interface {
	AttachContext(ctx context.Context)
}

// nopHost keeps App usable before a real Host is attached — and on mobile
// between Stop() and the next Start(), where a late goroutine would otherwise
// emit into a nil interface.
type nopHost struct{}

func (nopHost) Emit(string, interface{})                 {}
func (nopHost) Logf(string, string, ...interface{})      {}
func (nopHost) SaveMedia(string, []byte) (string, error) { return "", errNoHost }
func (nopHost) OpenFolder(string) error                  { return errNoHost }
