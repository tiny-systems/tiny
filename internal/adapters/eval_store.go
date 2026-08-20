package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tiny-systems/module/pkg/evals"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// EvalStore writes an eval to a file beside the project.
//
// A check that lives only in a conversation inspected the flow once; it does
// not guard it. Written down, the same check runs from `tiny eval` and from CI
// tomorrow, which is the whole difference between having looked and knowing.
//
// Plain YAML in a directory rather than a resource in the cluster: an eval is
// something a person edits and a repository reviews, and putting it in the
// cluster would make the check disappear with the project it was meant to
// outlive.
type EvalStore struct {
	// Dir is where evals land. Empty means ./evals, resolved against the
	// working directory tiny was started in — the user's project checkout.
	Dir string
}

func NewEvalStore(dir string) *EvalStore { return &EvalStore{Dir: dir} }

func (s *EvalStore) SaveEval(_ context.Context, _ string, spec evals.Spec) (string, error) {
	dir := s.Dir
	if dir == "" {
		dir = "evals"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	path := filepath.Join(dir, slug(spec.Name)+".yaml")

	// Each eval in its own file, so two agents writing checks for different
	// flows never collide, and a diff shows exactly which claim changed.
	encoded, err := evals.Marshal([]evals.Spec{spec})
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("# %s\n#\n# Run it: tiny eval %s\n", spec.Name, dir)
	if err := os.WriteFile(path, append([]byte(header), encoded...), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

var notFilename = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns a claim into a filename. The name is a sentence — "csv_decode
// keeps a quoted comma intact" — and the file should still be recognisable as
// that sentence when someone scans the directory.
func slug(name string) string {
	s := notFilename.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "eval"
	}
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

var _ sdktools.EvalStore = (*EvalStore)(nil)
