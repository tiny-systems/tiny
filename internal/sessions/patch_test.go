package sessions

import (
	"context"
	"testing"
)

func TestFilePatchRefusesPathsOutsideTheRepo(t *testing.T) {
	s := &Store{}
	for _, p := range []string{"", "/etc/passwd", "a\x00b"} {
		if _, _, err := s.FilePatch(context.Background(), "pod", p); err == nil {
			t.Errorf("FilePatch(%q) = nil error, want a refusal", p)
		}
	}
}
