package sessions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// fallbackShownName names a shown file whose path has no usable base name.
const fallbackShownName = "shown"

// MaxShownBytes caps what is pulled out of a pod for one shown file.
const MaxShownBytes = 25 << 20

// PickShown chooses one of a session's shown files: "" or "latest" for the
// newest, or its number as `tiny show` lists them (1 is the oldest).
func PickShown(shown []agentsv1.ShownFile, which string) (agentsv1.ShownFile, error) {
	if len(shown) == 0 {
		return agentsv1.ShownFile{}, fmt.Errorf("it hasn't shown any files")
	}
	if which == "" || which == "latest" {
		return shown[len(shown)-1], nil
	}
	n, err := strconv.Atoi(which)
	if err != nil || n < 1 || n > len(shown) {
		return agentsv1.ShownFile{}, fmt.Errorf("pick a number from 1 to %d, or latest", len(shown))
	}
	return shown[n-1], nil
}

// ShownName is the local file name for a shown file: its own base name,
// cleaned. The agent chose it, so it never picks the directory.
func ShownName(f agentsv1.ShownFile) string {
	name := strings.TrimLeft(path.Base(f.Path), ".")
	if name == "" || name == "/" {
		return fallbackShownName
	}
	return filepath.Base(name)
}

// FetchShown copies a shown file out of the session's pod to dest.
func (s *Store) FetchShown(ctx context.Context, pod string, f agentsv1.ShownFile, dest string) error {
	data, err := s.ReadFile(ctx, pod, f.Path, MaxShownBytes)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}

// ShownDir is where shown files land to be opened: per session, under the
// user's cache directory.
func ShownDir(session string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "tiny", "shown", filepath.Base(session))
}

// OpenLocal hands a local file to the desktop's default app.
func OpenLocal(p string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", p)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w (the file is saved there)", p, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
