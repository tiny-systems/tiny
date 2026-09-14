package sessions

// The tar we extract is written by an AGENT inside a pod: entry names are
// attacker-influenced. These tests are the guard rails — a crafted name or
// symlink must not write outside the destination, and a pull must never
// land on top of existing local work.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func tarball(t *testing.T, entries []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUntarRefusesPathEscape(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	raw := tarball(t,
		[]*tar.Header{{Name: "../escaped.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}},
		[]string{"bad"})

	if _, err := untarInto(bytes.NewReader(raw), dest, nil); err == nil {
		t.Fatal("a ../ entry must be refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escaped.txt")); err == nil {
		t.Fatal("the escaping file was written anyway")
	}
}

func TestUntarRefusesEscapingSymlink(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	raw := tarball(t,
		[]*tar.Header{{Name: "link", Linkname: "../../../../etc/passwd", Typeflag: tar.TypeSymlink}},
		[]string{""})

	if _, err := untarInto(bytes.NewReader(raw), dest, nil); err == nil {
		t.Fatal("a symlink pointing outside the destination must be refused")
	}
}

func TestUntarWritesNestedFiles(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	raw := tarball(t, []*tar.Header{
		{Name: "sub", Mode: 0o755, Typeflag: tar.TypeDir},
		{Name: "sub/app.py", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg},
	}, []string{"", "hello"})

	n, err := untarInto(bytes.NewReader(raw), dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("wrote %d files, want 1", n)
	}
	got, err := os.ReadFile(filepath.Join(dest, "sub", "app.py"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("nested file = %q, %v", got, err)
	}
}

func TestPullRefusesNonEmptyDestination(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("local work"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := emptyOrAbsent(dir); err == nil {
		t.Fatal("pulling into a directory with local files must be refused")
	}
	if err := emptyOrAbsent(filepath.Join(dir, "fresh")); err != nil {
		t.Fatalf("an absent directory is fine: %v", err)
	}
}
