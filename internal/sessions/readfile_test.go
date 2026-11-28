package sessions

import (
	"bytes"
	"testing"
)

func TestCapWriterNotesOverflow(t *testing.T) {
	var b bytes.Buffer
	w := &capWriter{buf: &b, limit: 4}
	_, _ = w.Write([]byte("abc"))
	if w.over {
		t.Fatal("over after 3 of 4 bytes")
	}
	_, _ = w.Write([]byte("de"))
	if !w.over || b.String() != "abcd" {
		t.Fatalf("over=%v buf=%q", w.over, b.String())
	}
}
