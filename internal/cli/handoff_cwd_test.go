package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptCwdReadsTheRecordedFolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte("{\"type\":\"mode\"}\n{\"cwd\":\"/Users/me/a/b-c\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := transcriptCwd(p)
	if err != nil || got != "/Users/me/a/b-c" {
		t.Fatalf("transcriptCwd = %q, %v", got, err)
	}
	stage, err := stageFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if e, _ := os.ReadDir(stage); len(e) != 1 {
		t.Errorf("staged %d files, want 1", len(e))
	}
}
