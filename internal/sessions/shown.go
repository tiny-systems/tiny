package sessions

import (
	"context"
	"fmt"
	"net/http"
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

// passiveTypes are the files OpenLocal hands to the desktop: ones the
// default app only displays. The agent chose the file and its name, so
// anything that could run (.command, .app, .html, .svg in a browser, a
// script) is saved and never opened. Each extension must also match the
// bytes, so a script renamed .png stays closed.
var passiveTypes = map[string]func(sniffed string) bool{
	".png":  func(t string) bool { return t == "image/png" },
	".jpg":  func(t string) bool { return t == "image/jpeg" },
	".jpeg": func(t string) bool { return t == "image/jpeg" },
	".gif":  func(t string) bool { return t == "image/gif" },
	".webp": func(t string) bool { return t == "image/webp" },
	".pdf":  func(t string) bool { return t == "application/pdf" },
	".txt":  isPlainText,
	".md":   isPlainText,
	".csv":  isPlainText,
	".log":  isPlainText,
	".json": isPlainText,
}

func isPlainText(t string) bool { return strings.HasPrefix(t, "text/plain") }

// Openable reports whether OpenLocal would open the file, judging by its
// name and its first bytes.
func Openable(name string, head []byte) bool {
	ok, known := passiveTypes[strings.ToLower(filepath.Ext(name))]
	return known && ok(http.DetectContentType(head))
}

// OpenLocal hands a local file to the desktop's default app, but only a
// passive type whose bytes match its extension (see passiveTypes). Any
// other file is left where it is and reported.
func OpenLocal(p string) error {
	head := make([]byte, 512)
	fh, err := os.Open(p)
	if err != nil {
		return err
	}
	n, _ := fh.Read(head)
	_ = fh.Close()
	if !Openable(p, head[:n]) {
		return fmt.Errorf("saved %s but didn't open it: only images, PDFs and plain text open by themselves, since an agent's file could be a program", p)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", p)
	case "windows":
		return fmt.Errorf("saved %s; open it from there", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w (the file is saved there)", p, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Printable drops control characters (C0, DEL, C1) from text an agent
// wrote, so printing it can't move the cursor or rewrite the terminal.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
