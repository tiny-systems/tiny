package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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
	var assetURL, assetName, sumsURL string
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			sumsURL = a.URL
		}
		if assetURL == "" && strings.Contains(a.Name, want) && (strings.HasSuffix(a.Name, ".tar.gz") || strings.HasSuffix(a.Name, ".zip")) {
			assetURL, assetName = a.URL, a.Name
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
	if err := applyUpdate(assetURL, assetName, sumsURL); err != nil {
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
	req, err := http.NewRequest(http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
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
func applyUpdate(archiveURL, archiveName, sumsURL string) error {
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
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Buffer the archive so it can be checksummed before anything from it
	// is executed or written over the running binary.
	archive, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, archiveName, sumsURL); err != nil {
		return err
	}

	bin, err := extractBinary(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(bin) }()

	// Same-directory temp so os.Rename is atomic (no cross-device copy).
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".tiny-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	in, err := os.Open(bin)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	_, cpErr := io.Copy(tmp, in)
	_ = in.Close()
	_ = tmp.Close()
	if cpErr != nil {
		_ = os.Remove(tmpName)
		return cpErr
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, exe)
}

// verifyChecksum compares the downloaded archive against the release's
// checksums.txt (goreleaser publishes it alongside the assets). A release
// without one, or one that omits this asset, fails loud — a self-update
// must never install bytes nothing vouches for.
func verifyChecksum(archive []byte, name, sumsURL string) error {
	if sumsURL == "" {
		return fmt.Errorf("release has no checksums.txt — refusing to self-update")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(sumsURL)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums download returned %d", resp.StatusCode)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			if fields[0] == sum {
				return nil
			}
			return fmt.Errorf("checksum mismatch for %s — expected %s, downloaded %s", name, fields[0], sum)
		}
	}
	return fmt.Errorf("checksums.txt does not list %s — refusing to self-update", name)
}

// extractBinary pulls the `tiny` binary out of a .tar.gz release archive to
// a temp file and returns its path. (Windows .zip handling is deferred; the
// Homebrew + curl|sh paths cover the common cases.)
func extractBinary(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("release archive is not gzip (windows .zip self-update not supported yet): %w", err)
	}
	defer func() { _ = gz.Close() }()
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
				_ = out.Close()
				_ = os.Remove(out.Name())
				return "", err
			}
			_ = out.Close()
			return out.Name(), nil
		}
	}
}

// isHomebrew detects a brew-managed install so the summary can note that
// brew's recorded version goes stale after an in-place self-update.
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
