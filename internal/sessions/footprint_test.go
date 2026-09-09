package sessions

// The footprint parser reads git's own words; these fixtures are the shapes
// it actually meets — binary files, untracked additions, a file both
// committed and dirty, and a workspace with no repo at all.

import "testing"

func TestParseFootprintReadsGitSections(t *testing.T) {
	raw := "@@BRANCH\ntiny/issue-7\n@@NUMSTAT\n" +
		"10\t2\tinternal/auth.go\n" +
		"-\t-\tassets/logo.png\n" +
		"@@DIRTY\n" +
		"40\t1\tinternal/auth.go\n" + // same file, bigger reach: wins
		"5\t0\tREADME.md\n" +
		"@@BUNDLES\n/workspace/outbox/tiny-issue-7.bundle\n"

	f := parseFootprint(raw)
	if f.Err != "" {
		t.Fatalf("unexpected err: %s", f.Err)
	}
	if f.Branch != "tiny/issue-7" {
		t.Errorf("branch = %q", f.Branch)
	}
	if len(f.Files) != 3 {
		t.Fatalf("files = %+v, want 3 distinct paths", f.Files)
	}
	var auth FileChange
	for _, c := range f.Files {
		if c.Path == "internal/auth.go" {
			auth = c
		}
	}
	if auth.Added != 40 || auth.Deleted != 1 {
		t.Errorf("dirty version should win for a file in both sections: %+v", auth)
	}
	// Binary (-1) must not poison the totals: 40 + 5 added, 1 deleted.
	if f.Added != 45 || f.Deleted != 1 {
		t.Errorf("totals = +%d/-%d, want +45/-1 (binary excluded)", f.Added, f.Deleted)
	}
	if len(f.Bundles) != 1 || f.Bundles[0] != "tiny-issue-7.bundle" {
		t.Errorf("bundles = %v, want the basename", f.Bundles)
	}
}

func TestParseFootprintNoRepo(t *testing.T) {
	f := parseFootprint("@@ERR no repo in workspace\n")
	if f.Err == "" {
		t.Fatal("a workspace without a repo must report why, not look empty")
	}
	if len(f.Files) != 0 {
		t.Errorf("files = %+v, want none", f.Files)
	}
}

func TestOverlapFindsSharedFiles(t *testing.T) {
	over := Overlap(map[string]Footprint{
		"night-run": {Files: []FileChange{{Path: "internal/auth.go"}, {Path: "a.go"}}},
		"api-fix":   {Files: []FileChange{{Path: "internal/auth.go"}, {Path: "b.go"}}},
		"readme":    {Files: []FileChange{{Path: "README.md"}}},
	})
	if len(over) != 1 {
		t.Fatalf("overlap = %v, want only the shared file", over)
	}
	names := over["internal/auth.go"]
	if len(names) != 2 {
		t.Fatalf("internal/auth.go touched by %v, want both sessions", names)
	}
}
