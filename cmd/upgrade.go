package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const releasesAPI = "https://api.github.com/repos/tiny-systems/tiny/releases/latest"

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update", "self-update"},
		Short:   "Update tiny to the latest release",
		RunE:    runUpgrade,
	}
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func runUpgrade(cmd *cobra.Command, _ []string) error {
	current := cmd.Root().Version

	fmt.Println("  " + styleSubtle.Render("checking for updates…"))
	rel, err := latestRelease()
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	cur := strings.TrimPrefix(current, "v")
	if cur != "dev" && cur == latest {
		fmt.Printf("  %s already on the latest version (%s)\n", styleOK.Render("✓"), styleTitle.Render(current))
		return nil
	}

	want := fmt.Sprintf("_%s_%s", runtime.GOOS, runtime.GOARCH)
	var assetURL string
	for _, a := range rel.Assets {
		if strings.Contains(a.Name, want) && (strings.HasSuffix(a.Name, ".tar.gz") || strings.HasSuffix(a.Name, ".zip")) {
			assetURL = a.URL
			break
		}
	}
	if assetURL == "" {
		return fmt.Errorf("no release asset for %s/%s in %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}

	// We self-update in place even on Homebrew installs (we resolve the symlink
	// and overwrite the real binary, so brew's bin/ link still points at it).
	// brew's recorded version goes stale — harmless — so just note it after.
	brew := false
	if exe, _ := os.Executable(); isHomebrew(exe) {
		brew = true
	}

	fmt.Printf("  %s %s → %s\n", styleKey.Render("upgrade"), styleSubtle.Render(current), styleTitle.Render(rel.TagName))
	if err := applyUpdate(assetURL); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}
	fmt.Printf("  %s updated to %s\n", styleOK.Render("✓"), styleTitle.Render(rel.TagName))
	if brew {
		fmt.Println("  " + styleSubtle.Render("(Homebrew still lists the old version — `brew upgrade tiny` reconciles it; harmless to leave)"))
	}
	return nil
}

func latestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, releasesAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github returned %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// applyUpdate downloads the release archive, extracts the `tiny` binary, and
// swaps it over the running executable. On unix a rename over a running
// binary is safe; we write to a temp file in the same dir first so the
// replace is atomic and stays on one filesystem.
func applyUpdate(archiveURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve symlinks so we overwrite the actual binary (e.g. Homebrew's
	// Cellar target), not a symlink in bin/ — that keeps brew's link valid.
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(archiveURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	bin, err := extractBinary(resp.Body)
	if err != nil {
		return err
	}
	defer os.Remove(bin)

	// Same-directory temp so os.Rename is atomic (no cross-device copy).
	dir := exe[:strings.LastIndex(exe, "/")+1]
	tmp, err := os.CreateTemp(dir, ".tiny-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	in, err := os.Open(bin)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	_, cpErr := io.Copy(tmp, in)
	in.Close()
	tmp.Close()
	if cpErr != nil {
		os.Remove(tmpName)
		return cpErr
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, exe)
}

// extractBinary pulls the `tiny` binary out of a .tar.gz release archive to
// a temp file and returns its path. (Windows .zip handling is deferred; the
// Homebrew + curl|sh paths cover the common cases.)
func extractBinary(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("release archive is not gzip (windows .zip self-update not supported yet): %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("no `tiny` binary in archive")
		}
		if err != nil {
			return "", err
		}
		if h.Typeflag == tar.TypeReg && (h.Name == "tiny" || strings.HasSuffix(h.Name, "/tiny")) {
			out, err := os.CreateTemp("", "tiny-bin-*")
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				os.Remove(out.Name())
				return "", err
			}
			out.Close()
			return out.Name(), nil
		}
	}
}

// isHomebrew reports whether the executable lives under a Homebrew prefix,
// so `tiny upgrade` can defer to `brew upgrade` instead of fighting it.
func isHomebrew(exe string) bool {
	if exe == "" {
		return false
	}
	if strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/") {
		return true
	}
	// Resolve a brew symlink target if brew is present.
	if out, err := exec.Command("brew", "--prefix").Output(); err == nil {
		prefix := strings.TrimSpace(string(out))
		if prefix != "" && strings.HasPrefix(exe, prefix) {
			return true
		}
	}
	return false
}
