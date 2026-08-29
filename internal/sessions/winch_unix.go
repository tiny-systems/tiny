//go:build !windows

package sessions

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize wires terminal-resize signals into ch; the returned stop
// undoes it. Windows has no SIGWINCH — its build polls nothing and keeps
// the initial size (see winch_windows.go).
func notifyResize(ch chan os.Signal) (stop func()) {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}
