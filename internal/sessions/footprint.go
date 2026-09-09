/*
A session's footprint is the blast radius of its work: which files it has
touched, how far, on what branch, and what is waiting in its outbox. It is
read live from the workspace over the exec API — no log parsing, no stored
state, just git answering the question it already knows.

The fleet screen, `tiny diff` and the web add-on all read it through here,
so there is one definition of "what did this session change".
*/
package sessions

import (
	"context"
	"io"
	"strconv"
	"strings"
)

// FileChange is one touched file and its size of change. Added and Deleted
// are -1 when git reports the file as binary.
type FileChange struct {
	Path    string
	Added   int
	Deleted int
	Staged  bool // in the index or committed on the branch, not just dirty
}

// Footprint is everything the fleet needs to say how far a session has
// reached into a repo.
type Footprint struct {
	Branch  string
	Files   []FileChange
	Added   int
	Deleted int
	// Bundles are pending outbox files — work finished but not yet pushed.
	Bundles []string
	// Err is set when the workspace has no repo (or git failed); the caller
	// reports it as "no repo" rather than an error, since an idle session
	// legitimately has nothing.
	Err string
}

// Touched lists just the paths, for overlap comparisons between sessions.
func (f Footprint) Touched() []string {
	out := make([]string, 0, len(f.Files))
	for _, c := range f.Files {
		out = append(out, c.Path)
	}
	return out
}

// footprintScript asks git three questions in one exec: the branch, the
// numstat of everything not yet on the base branch (committed AND dirty),
// and the pending bundles. Sections are marked so one read parses cleanly.
const footprintScript = `
cd /workspace/repo 2>/dev/null || { echo "@@ERR no repo in workspace"; exit 0; }
echo "@@BRANCH"
git rev-parse --abbrev-ref HEAD 2>/dev/null
echo "@@NUMSTAT"
# Committed work relative to the default branch, plus uncommitted changes.
base=$(git rev-parse --verify -q origin/main || git rev-parse --verify -q origin/master || git rev-parse --verify -q main || git rev-parse --verify -q master)
if [ -n "$base" ]; then git diff --numstat "$base"...HEAD 2>/dev/null; fi
echo "@@DIRTY"
git diff --numstat HEAD 2>/dev/null
git ls-files --others --exclude-standard 2>/dev/null | while read -r f; do
  printf '%s\t%s\t%s\n' "$(wc -l < "$f" 2>/dev/null || echo 0)" 0 "$f"
done
echo "@@BUNDLES"
ls /workspace/outbox/*.bundle 2>/dev/null || true
`

// Footprint reads a running session's blast radius from its pod.
func (s *Store) Footprint(ctx context.Context, pod string) Footprint {
	var out strings.Builder
	if err := s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c", footprintScript}, nil, &out, io.Discard); err != nil {
		return Footprint{Err: "cannot read workspace: " + err.Error()}
	}
	return parseFootprint(out.String())
}

// parseFootprint turns the script's sectioned output into a Footprint.
// Split out so it is testable without a cluster.
const (
	secBranch  = "@@BRANCH"
	secNumstat = "@@NUMSTAT"
	secDirty   = "@@DIRTY"
	secBundles = "@@BUNDLES"
)

func parseFootprint(raw string) Footprint {
	var f Footprint
	seen := map[string]int{} // path -> index in f.Files, so dirty updates committed
	section := ""
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@ERR "):
			f.Err = strings.TrimPrefix(line, "@@ERR ")
			return f
		case line == secBranch, line == secNumstat, line == secDirty, line == secBundles:
			section = line
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch section {
		case secBranch:
			if f.Branch == "" {
				f.Branch = strings.TrimSpace(line)
			}
		case secNumstat, secDirty:
			c, ok := parseNumstat(line)
			if !ok {
				continue
			}
			c.Staged = section == secNumstat
			if i, dup := seen[c.Path]; dup {
				// A file both committed and dirty counts once, with the
				// larger reach — the point is blast radius, not accounting.
				if c.Added+c.Deleted > f.Files[i].Added+f.Files[i].Deleted {
					f.Files[i] = c
				}
				continue
			}
			seen[c.Path] = len(f.Files)
			f.Files = append(f.Files, c)
		case secBundles:
			name := strings.TrimSpace(line)
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			f.Bundles = append(f.Bundles, name)
		}
	}
	for _, c := range f.Files {
		if c.Added > 0 {
			f.Added += c.Added
		}
		if c.Deleted > 0 {
			f.Deleted += c.Deleted
		}
	}
	return f
}

// parseNumstat reads git's "added\tdeleted\tpath" line; binary files report
// "-" for both, which becomes -1.
func parseNumstat(line string) (FileChange, bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return FileChange{}, false
	}
	num := func(s string) int {
		if s == "-" {
			return -1
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0
		}
		return n
	}
	return FileChange{
		Path:    strings.TrimSpace(parts[2]),
		Added:   num(parts[0]),
		Deleted: num(parts[1]),
	}, true
}

// Overlap reports files touched by more than one session: path -> the
// sessions touching it. The team question the fleet screen cannot answer.
func Overlap(bySession map[string]Footprint) map[string][]string {
	hits := map[string][]string{}
	for name, f := range bySession {
		for _, path := range f.Touched() {
			hits[path] = append(hits[path], name)
		}
	}
	for path, names := range hits {
		if len(names) < 2 {
			delete(hits, path)
		}
	}
	return hits
}
