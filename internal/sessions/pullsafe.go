package sessions

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

/*
A pulled workspace comes from a pod the agent controls. Git runs code
from a repository's own files (hooks; config such as core.fsmonitor,
filters, textconv, includes), so a .git copied as it is would run the
agent's code on this machine at the first git command there.

Sanitizing such a .git is a losing game, so nothing of it is copied:

  - no tar entry with a path component named .git (any case) is written;
  - any other directory git would accept as a repository (HEAD next to
    objects, refs or commondir: a bare repo, a stray gitdir) is deleted;
  - the history arrives as a `git bundle` (objects and refs, data only)
    and is fetched into a fresh `git init` here, with HEAD, the index and
    the origin remote written by this code, never read from the pod.

Every step fails closed: an error removes the pull.
*/

// PullResult says what a pull brought back.
type PullResult struct {
	Files int
	// History is true when the project's git history came too.
	History bool
	// Note says why the history didn't, or what was left out.
	Note string
}

var (
	// plainValue has nothing git would unquote or unescape.
	plainValue = regexp.MustCompile(`^[^"\;#\s][^"\;#]*$`)
	// safeURL is a transport that connects rather than runs a command.
	safeURL = regexp.MustCompile(`^(https?://|ssh://|git://|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:|/)`)
	// objectID is a full SHA-1 or SHA-256 commit id.
	objectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// isGitPath reports whether a tar path has a .git component, in any case.
func isGitPath(name string) bool {
	for p := range strings.SplitSeq(filepath.ToSlash(name), "/") {
		if strings.EqualFold(p, ".git") {
			return true
		}
	}
	return false
}

// removeRepoLike deletes every directory under dest that git would take
// for a repository, and returns how many.
func removeRepoLike(dest string) (int, error) {
	n := 0
	err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || !looksLikeGitDir(path) {
			return nil
		}
		n++
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return fs.SkipDir
	})
	return n, err
}

// looksLikeGitDir is generous on purpose: HEAD beside objects, refs or
// commondir, names compared without case.
func looksLikeGitDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	head, other := false, false
	for _, e := range entries {
		switch strings.ToLower(e.Name()) {
		case "head":
			head = true
		case "objects", "refs", "commondir":
			other = true
		}
	}
	return head && other
}

// historyScript prints the facts the local rebuild needs, then nothing
// else; the bundle is a separate exec so the two streams never mix.
const historyScript = `
cd "$1" 2>/dev/null || exit 0
git rev-parse -q --verify HEAD >/dev/null 2>&1 || exit 0
echo "HEAD $(git rev-parse HEAD)"
echo "BRANCH $(git symbolic-ref -q --short HEAD)"
echo "ORIGIN $(git remote get-url origin 2>/dev/null)"
echo "SHALLOW $(cat "$(git rev-parse --git-dir)/shallow" 2>/dev/null | tr '\n' ' ')"
`

// pullHistory brings srcDir's git history into dest as a fresh repository.
func (s *Store) pullHistory(ctx context.Context, pod, srcDir, dest string) (bool, string, error) {
	var meta strings.Builder
	if err := s.execStream(ctx, pod, agentContainer, []string{"sh", "-c", historyScript, "history", srcDir}, nil, &meta, io.Discard); err != nil {
		return false, "", fmt.Errorf("read history: %w", err)
	}
	facts := map[string]string{}
	for line := range strings.SplitSeq(meta.String(), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		facts[k] = strings.TrimSpace(v)
	}
	if !objectID.MatchString(facts["HEAD"]) {
		return false, "no git history in the session", nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return false, "history left out: git isn't installed on this computer", nil
	}

	bundle, err := os.CreateTemp("", "tiny-pull-*.bundle")
	if err != nil {
		return false, "", err
	}
	defer func() { _ = os.Remove(bundle.Name()) }()
	var stderr strings.Builder
	err = s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c", `cd "$1" && exec git bundle create - --all`, "bundle", srcDir}, nil, bundle, &stderr)
	if cerr := bundle.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, "", fmt.Errorf("bundle history: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	var shallow []string
	for id := range strings.FieldsSeq(facts["SHALLOW"]) {
		if !objectID.MatchString(id) {
			return false, "", fmt.Errorf("session reported a malformed shallow commit %q", id)
		}
		shallow = append(shallow, id)
	}
	return true, "", rebuildHistory(ctx, dest, bundle.Name(), facts["HEAD"], facts["BRANCH"], facts["ORIGIN"], shallow)
}

// rebuildHistory makes dest a repository holding the bundle's history,
// HEAD on branch (or detached at head), the index matching HEAD so the
// pulled files show as the session's uncommitted changes. shallow lists
// the commits a shallow clone stops at (only validated ids), so a bundle
// from one has the boundary it needs.
func rebuildHistory(ctx context.Context, dest, bundle, head, branch, origin string, shallow []string) error {
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dest}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := git("init", "-q"); err != nil {
		return err
	}
	if len(shallow) > 0 {
		if err := os.WriteFile(filepath.Join(dest, ".git", "shallow"), []byte(strings.Join(shallow, "\n")+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := git("fetch", "-q", "--no-tags", "--update-head-ok", bundle, "+refs/*:refs/*"); err != nil {
		return err
	}
	if branch != "" && git("check-ref-format", "--branch", branch) == nil && git("rev-parse", "-q", "--verify", "refs/heads/"+branch) == nil {
		if err := git("symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
			return err
		}
	} else if err := git("update-ref", "--no-deref", "HEAD", head); err != nil {
		return err
	}
	if err := git("reset", "-q"); err != nil {
		return err
	}
	if plainValue.MatchString(origin) && safeURL.MatchString(origin) {
		return git("remote", "add", "origin", origin)
	}
	return nil
}
