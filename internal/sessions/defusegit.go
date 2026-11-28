package sessions

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

/*
A pulled workspace comes from a pod the agent controls, and so does its
.git. Git runs code from a repository's own files: hooks, and config keys
such as core.fsmonitor, core.sshCommand, core.pager, filter.*.clean,
diff.*.textconv, credential.helper or an include of another file. The
first `git status` in the pulled folder would then run whatever the agent
wrote, on the laptop, outside every sandbox.

defuseGit leaves a repository git can still read (objects, refs, the
index, remotes and branch tracking) and nothing it would execute.
*/

// keptConfig is the allowlist: section -> keys kept. Anything else,
// including include and includeIf, is dropped.
var keptConfig = map[string]map[string]bool{
	"core":       {"repositoryformatversion": true, "bare": true, "logallrefupdates": true},
	"extensions": {"objectformat": true, "refstorage": true},
	"remote":     {"url": true, "pushurl": true, "fetch": true},
	"branch":     {"remote": true, "merge": true},
}

// worktreeConfig is git's per-worktree config file.
const worktreeConfig = "config.worktree"

// defuseGit makes dest/.git (and any submodule repositories under it)
// safe to run git in. A .git that is a file or a link (it could point at
// another repository on this machine) is removed.
func defuseGit(dest string) error {
	gitDir := filepath.Join(dest, ".git")
	st, err := os.Lstat(gitDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return os.Remove(gitDir)
	}
	// Linked worktrees point outside through commondir/gitdir files.
	for _, p := range []string{"worktrees", "commondir", worktreeConfig} {
		if err := os.RemoveAll(filepath.Join(gitDir, p)); err != nil {
			return err
		}
	}
	return filepath.WalkDir(gitDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && d.Name() == "hooks":
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				return err
			}
			return fs.SkipDir
		case d.IsDir() && d.Name() == "objects":
			return fs.SkipDir // content only, and large
		case !d.IsDir() && d.Name() == worktreeConfig:
			return os.Remove(path)
		case !d.IsDir() && d.Name() == "config":
			return rewriteConfig(path)
		}
		return nil
	})
}

// rewriteConfig keeps only the allowlisted keys of a git config file.
func rewriteConfig(path string) error {
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		_ = os.Remove(path)
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	out := safeGitConfig(f)
	_ = f.Close()
	return os.WriteFile(path, []byte(out), 0o644)
}

func safeGitConfig(r interface{ Read([]byte) (int, error) }) string {
	var b strings.Builder
	section, header := "", ""
	wrote := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	skipping := false // inside a value continued with a trailing backslash
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if skipping {
			skipping = strings.HasSuffix(line, `\`)
			continue
		}
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				section = ""
				continue
			}
			header = line[:end+1]
			name := strings.TrimSpace(header[1 : len(header)-1])
			if i := strings.IndexAny(name, " \t\"."); i >= 0 {
				name = name[:i]
			}
			section = strings.ToLower(name)
			wrote = false
			continue
		}
		if strings.HasSuffix(line, `\`) {
			skipping = true
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if !keptConfig[section][key] {
			continue
		}
		// Transports that run a command instead of connecting.
		if (key == "url" || key == "pushurl") && (strings.HasPrefix(val, "ext::") || strings.HasPrefix(val, "fd::")) {
			continue
		}
		if !wrote {
			b.WriteString(header + "\n")
			wrote = true
		}
		b.WriteString("\t" + key + " = " + val + "\n")
	}
	return b.String()
}
