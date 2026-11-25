package main

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type manifest struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Version          string `json:"version"`
		ShortDescription string `json:"shortDescription"`
		Platforms        []struct {
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
			URI    string `json:"uri"`
			SHA256 string `json:"sha256"`
			Bin    string `json:"bin"`
		} `json:"platforms"`
	} `json:"spec"`
}

var platforms = []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}

func archive(version, platform string) string {
	osName, arch, _ := strings.Cut(platform, "/")
	ext := ".tar.gz"
	if osName == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("tiny_%s_%s_%s%s", version, osName, arch, ext)
}

// The template must point every platform at the archive goreleaser
// publishes (tiny_<version without v>_<os>_<arch>) with the binary name
// inside it, or `kubectl krew install tiny` breaks on the next release.
func TestTemplateMatchesReleaseAssets(t *testing.T) {
	raw, err := os.ReadFile("../../.krew.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// One distinct checksum per archive, deliberately all digits for one
	// of them: it must still come out as a string.
	sum := func(uri string) (string, error) {
		if strings.Contains(uri, "linux_amd64") {
			return strings.Repeat("1", 64), nil
		}
		return fmt.Sprintf("%064x", len(path.Base(uri))), nil
	}
	out, err := Render(raw, "v1.2.3", sum)
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatalf("rendered manifest is not YAML: %v\n%s", err, out)
	}
	if m.Metadata.Name != "tiny" || m.Spec.Version != "v1.2.3" {
		t.Fatalf("name=%q version=%q", m.Metadata.Name, m.Spec.Version)
	}
	if n := len(m.Spec.ShortDescription); n == 0 || n > 50 {
		t.Fatalf("shortDescription is %d characters; krew-index wants a short one", n)
	}
	seen := map[string]bool{}
	for _, p := range m.Spec.Platforms {
		key := p.Selector.MatchLabels["os"] + "/" + p.Selector.MatchLabels["arch"]
		seen[key] = true
		want := "https://github.com/tiny-systems/tiny/releases/download/v1.2.3/" + archive("1.2.3", key)
		if p.URI != want {
			t.Errorf("%s uri = %s\nwant %s", key, p.URI, want)
		}
		bin := "tiny"
		if strings.HasPrefix(key, "windows/") {
			bin = "tiny.exe"
		}
		if p.Bin != bin {
			t.Errorf("%s bin = %q, want %q", key, p.Bin, bin)
		}
		if wantSum, _ := sum(p.URI); p.SHA256 != wantSum {
			t.Errorf("%s sha256 = %q, want %q", key, p.SHA256, wantSum)
		}
	}
	for _, p := range platforms {
		if !seen[p] {
			t.Errorf("platform %s missing from .krew.yaml", p)
		}
	}
}

// End to end through checksums.txt, as the release workflow runs it, and
// a missing archive is an error rather than a manifest with a hole.
func TestRunReadsChecksums(t *testing.T) {
	dir := t.TempDir()
	var sums bytes.Buffer
	for i, p := range platforms {
		fmt.Fprintf(&sums, "%064x  %s\n", i+1, archive("1.2.3", p))
	}
	sumsPath := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(sumsPath, sums.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run("v1.2.3", sumsPath, "../../.krew.yaml", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("%064x", 3)) {
		t.Fatal("linux/amd64 checksum missing from the manifest")
	}

	partial := strings.Join(strings.Split(sums.String(), "\n")[:2], "\n")
	if err := os.WriteFile(sumsPath, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("v1.2.3", sumsPath, "../../.krew.yaml", &bytes.Buffer{}); err == nil {
		t.Fatal("a checksum missing for an archive must fail")
	}
	if err := run("1.2.3", sumsPath, "../../.krew.yaml", &bytes.Buffer{}); err == nil {
		t.Fatal("a tag without v must fail")
	}
}
