//go:build !windows

package browser

// Chromium's --window-name argument remains active on non-Windows platforms;
// native chrome styling is intentionally a no-op.
func startVisualIdentity(_ Process, _ VisualIdentity) {}
