package sessions

// The paste interceptor sits on every keystroke of an attached session —
// a byte it mangles is a keystroke the human loses. These cover the state
// machine's sharp edges: markers split across reads, pastes that are not
// file drops, and input with no pastes at all.

import (
	"bytes"
	"testing"
)

func feed(t *testing.T, p *pasteInterceptor, chunks ...[]byte) []byte {
	t.Helper()
	out := make([]byte, 0, 64)
	for _, c := range chunks {
		p.consume(c)
		out = append(out, p.buf...)
		p.buf = nil
	}
	return out
}

func TestInterceptorPassesPlainInputThrough(t *testing.T) {
	p := &pasteInterceptor{}
	got := feed(t, p, []byte("hello"), []byte(" world\r"))
	if string(got) != "hello world\r" {
		t.Fatalf("mangled plain input: %q", got)
	}
}

func TestInterceptorKeepsNonFilePastesIntact(t *testing.T) {
	p := &pasteInterceptor{}
	in := append(append(append([]byte{}, pasteStart...), []byte("just some pasted text")...), pasteEnd...)
	got := feed(t, p, in)
	if !bytes.Equal(got, in) {
		t.Fatalf("non-file paste rewritten: %q", got)
	}
}

func TestInterceptorSurvivesMarkersSplitAcrossReads(t *testing.T) {
	p := &pasteInterceptor{}
	// Start marker split 3+3, content split, end marker split 2+4.
	got := feed(t, p,
		pasteStart[:3], pasteStart[3:],
		[]byte("/no/such/file-"), []byte("here"),
		pasteEnd[:2], pasteEnd[2:],
	)
	want := append(append(append([]byte{}, pasteStart...), []byte("/no/such/file-here")...), pasteEnd...)
	if !bytes.Equal(got, want) {
		t.Fatalf("split markers broke the paste:\n got %q\nwant %q", got, want)
	}
}

func TestInterceptorEscHeldOnlyWhilePotentialMarker(t *testing.T) {
	p := &pasteInterceptor{}
	// A lone ESC (arrow keys etc.) must still come through once the next
	// read disambiguates it.
	got := feed(t, p, []byte("\x1b"), []byte("[A"))
	if string(got) != "\x1b[A" {
		t.Fatalf("escape sequence eaten: %q", got)
	}
}
