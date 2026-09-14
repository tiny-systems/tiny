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
	"strings"
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

// tarEntry is one archive member for the hostile-tar tests.
type tarEntry struct {
	name     string
	typ      byte
	linkname string
	body     string
}

func buildTar(t *testing.T, entries []tarEntry) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.linkname, Mode: 0o644, Size: int64(len(e.body))}
		if e.typ == tar.TypeDir {
			hdr.Mode = 0o755
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
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
	return bytes.NewReader(buf.Bytes())
}

// The finding that prompted this: filepath.Join absorbs a leading separator,
// so an absolute link target used to normalise to something that looked
// contained. The two-entry form is the actual exploit — plant the link, then
// write through it.
func TestUntarRefusesAbsoluteSymlinkTarget(t *testing.T) {
	dest := t.TempDir()
	outside := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := untarInto(buildTar(t, []tarEntry{
		{name: "evil", typ: tar.TypeSymlink, linkname: outside},
		{name: "evil", typ: tar.TypeReg, body: "attacker-key\n"},
	}), dest, nil)
	if err == nil {
		t.Fatal("expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "absolute target") {
		t.Fatalf("wrong reason: %v", err)
	}
	got, rerr := os.ReadFile(outside)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original\n" {
		t.Fatalf("file outside destination was overwritten: %q", got)
	}
	if _, serr := os.Lstat(filepath.Join(dest, "evil")); serr == nil {
		t.Fatal("symlink was created despite refusal")
	}
}

// A relative link that climbs out is the same attack spelled differently,
// and must not produce a write outside either.
func TestUntarRefusesRelativeEscapeWriteThrough(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "secret")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := untarInto(buildTar(t, []tarEntry{
		{name: "link", typ: tar.TypeSymlink, linkname: "../secret"},
		{name: "link", typ: tar.TypeReg, body: "pwned\n"},
	}), dest, nil)
	if err == nil {
		t.Fatal("expected refusal, got nil")
	}
	got, rerr := os.ReadFile(outside)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original\n" {
		t.Fatalf("file outside destination was overwritten: %q", got)
	}
}

// A symlinked DIRECTORY component is the case O_NOFOLLOW alone misses: it
// only guards the final element, so the parent chain is checked too.
func TestUntarRefusesSymlinkedParentDirectory(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	outside := filepath.Join(base, "etc")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(outside, "passwd")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// "sub" is a relative link that stays inside by name resolution only
	// because we hand-place it; untarInto must still refuse the write.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "sub")); err != nil {
		t.Fatal(err)
	}
	_, err := untarInto(buildTar(t, []tarEntry{
		{name: "sub/passwd", typ: tar.TypeReg, body: "pwned\n"},
	}), dest, nil)
	if err == nil {
		t.Fatal("expected refusal, got nil")
	}
	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original\n" {
		t.Fatalf("write traversed a symlinked parent: %q", got)
	}
}

// Legitimate relative symlinks inside the tree still work — a repo may
// contain them and pull must not mangle the workspace.
func TestUntarKeepsContainedSymlink(t *testing.T) {
	dest := t.TempDir()
	n, err := untarInto(buildTar(t, []tarEntry{
		{name: "real.txt", typ: tar.TypeReg, body: "hello\n"},
		{name: "nested/", typ: tar.TypeDir},
		{name: "nested/link.txt", typ: tar.TypeSymlink, linkname: "../real.txt"},
	}), dest, nil)
	if err != nil {
		t.Fatalf("contained symlink refused: %v", err)
	}
	if n == 0 {
		t.Fatal("no files counted")
	}
	got, err := os.ReadFile(filepath.Join(dest, "nested/link.txt"))
	if err != nil {
		t.Fatalf("symlink not usable: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("symlink resolved wrong: %q", got)
	}
}
