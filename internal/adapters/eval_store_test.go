package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tiny-systems/module/pkg/evals"
)

// The file has to be one a person recognises in a directory listing and can
// edit by hand — otherwise it is a black box that only tooling can touch.
func TestSavedEvalIsReadableAndRunnableAgain(t *testing.T) {
	dir := t.TempDir()
	store := NewEvalStore(filepath.Join(dir, "evals"))

	spec := evals.Spec{
		Name:    "csv_decode keeps a quoted comma intact",
		Flow:    "csv-smoke",
		Trigger: evals.Trigger{Node: "signal-1", Port: "_control", Data: map[string]interface{}{"send": true}},
		Timeout: evals.Duration(45 * time.Second),
		Expect:  evals.Expect{Arrives: []evals.Arrival{{At: "debug-1:in", Path: "$.count", Equals: 2}}},
	}

	path, err := store.SaveEval(context.Background(), "proj", spec)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasSuffix(path, "csv-decode-keeps-a-quoted-comma-intact.yaml") {
		t.Fatalf("path = %q — the claim should still be readable in the filename", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "tiny eval") {
		t.Error("the file does not say how to run it")
	}

	back, err := evals.Parse(path, data)
	if err != nil {
		t.Fatalf("what it wrote does not parse: %v\n%s", err, data)
	}
	if len(back) != 1 || back[0].Name != spec.Name {
		t.Fatalf("round trip changed the eval: %+v", back)
	}
}

func TestSlugStaysAFilename(t *testing.T) {
	for name, want := range map[string]string{
		"csv_decode keeps a comma": "csv-decode-keeps-a-comma",
		"  Mixed CASE / slashes  ": "mixed-case-slashes",
		"":                         "eval",
		"!!!":                      "eval",
	} {
		if got := slug(name); got != want {
			t.Errorf("slug(%q) = %q, want %q", name, got, want)
		}
	}
}
