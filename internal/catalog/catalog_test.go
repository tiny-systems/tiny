package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestResolveRejectsUnqualified proves a bare (non-workspace-qualified) module
// name is rejected before any network call — tinysystems is one provider among
// others, so a bare name is ambiguous.
func TestResolveRejectsUnqualified(t *testing.T) {
	for _, name := range []string{"http-module", "http-module-v0", "common-module", ""} {
		_, err := resolve(context.Background(), "http://catalog.invalid", name)
		if err == nil {
			t.Fatalf("resolve(%q) = nil error, want workspace-qualified rejection", name)
		}
		if !strings.Contains(err.Error(), "workspace-qualified") {
			t.Fatalf("resolve(%q) error = %q, want workspace-qualified message", name, err)
		}
	}
}
