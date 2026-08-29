//go:build windows

package sessions

import "os"

// notifyResize is a no-op on Windows: there is no SIGWINCH, so an attach
// keeps its initial terminal size. Good enough for the rare Windows CLI.
func notifyResize(_ chan os.Signal) (stop func()) {
	return func() {}
}
