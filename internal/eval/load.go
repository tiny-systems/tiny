package eval

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tiny-systems/module/pkg/evals"
)

// DefaultDir is where evals live when none is named. A directory rather than
// one file, because evals accumulate per flow and a single file becomes the
// thing nobody wants to edit.
const DefaultDir = "evals"

// Load reads eval specs from a file or a directory of them. Order is stable so
// two runs report in the same sequence.
func Load(path string) ([]evals.Spec, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read evals from %s: %w", path, err)
	}

	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return evals.Parse(path, data)
	}

	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isEvalFile(p) {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var specs []evals.Spec
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		parsed, err := evals.Parse(f, data)
		if err != nil {
			return nil, err
		}
		specs = append(specs, parsed...)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no evals found in %s — files must end in .yaml or .yml", path)
	}
	return specs, nil
}

func isEvalFile(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".yaml" || ext == ".yml"
}
