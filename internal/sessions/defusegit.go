package sessions

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

/*
A pulled workspace comes from a pod the agent controls, and so does every
git repository in it. Git runs code from a repository's own files: hooks,
and config keys such as core.fsmonitor, core.sshCommand, filter.*.clean,
diff.*.textconv, credential.helper or an include of another file. The
first `git status` in the pulled folder (or in any repository nested in
it, bare ones included) would then run whatever the agent wrote, on the
laptop, outside every sandbox.

defuseGit walks the whole pulled tree. Every git directory it finds keeps
what git reads (objects, refs, the index, remotes, branch tracking) and
loses what git would run. Symlinks inside a git directory are removed, so
nothing can point git at files the sanitizer didn't rewrite.
*/

const (
	// worktreeConfig is git's per-worktree config file.
	worktreeConfig = "config.worktree"
	// objectsDir is a repository's object store.
	objectsDir = "objects"
)

// keptConfig is the allowlist: section -> keys kept. Anything else,
// including include and includeIf, is dropped.
var keptConfig = map[string]map[string]bool{
	"core":       {"repositoryformatversion": true, "bare": true, "logallrefupdates": true},
	"extensions": {"objectformat": true, "refstorage": true},
	"remote":     {"url": true, "pushurl": true, "fetch": true},
	"branch":     {"remote": true, "merge": true},
}

var (
	// subsectionOK is a remote or branch name safe to re-emit unquoted
	// inside the header's quotes.
	subsectionOK = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,200}$`)
	// plainValue has nothing git would unquote or unescape.
	plainValue = regexp.MustCompile(`^[^"\;#\s][^"\;#]*$`)
	// safeURL is a transport that connects rather than runs a command.
	safeURL = regexp.MustCompile(`^(https?://|ssh://|git://|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:|/)`)
)

// defuseGit makes every git repository under dest safe to run git in.
func defuseGit(dest string) error {
	var gitDirs []string
	err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" && d.Type()&fs.ModeDir == 0 {
			// A gitfile or a link can point at another repository on
			// this machine.
			return os.Remove(path)
		}
		if d.IsDir() && isGitDir(path) {
			gitDirs = append(gitDirs, path)
			return fs.SkipDir // defused below, whole
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, g := range gitDirs {
		if err := defuseGitDir(g); err != nil {
			return err
		}
	}
	return nil
}

// isGitDir is what git itself accepts as a repository directory: HEAD,
// plus objects and refs. Bare repositories and .git directories alike.
func isGitDir(dir string) bool {
	for _, p := range []string{"HEAD", objectsDir, "refs"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err != nil {
			return false
		}
	}
	return true
}

// defuseGitDir cleans one git directory and any nested in it (submodules
// under modules/).
func defuseGitDir(g string) error {
	// Linked worktrees point outside through commondir/gitdir files.
	for _, p := range []string{"worktrees", "commondir", worktreeConfig} {
		if err := os.RemoveAll(filepath.Join(g, p)); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(g, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return os.Remove(path)
		}
		parent := filepath.Dir(path)
		switch {
		case d.IsDir() && d.Name() == "hooks":
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return fs.SkipDir
		case d.IsDir() && d.Name() == objectsDir && path != g && isGitDir(parent):
			return fs.SkipDir // the object store: content only, and large
		case !d.IsDir() && d.Name() == worktreeConfig:
			return os.Remove(path)
		case !d.IsDir() && d.Name() == "config" && isGitDir(parent):
			return rewriteConfig(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// An empty, real hooks directory where git looks for one.
	return filepath.WalkDir(g, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == objectsDir && isGitDir(filepath.Dir(path)) {
			return fs.SkipDir
		}
		if d.IsDir() && isGitDir(path) {
			if err := os.Mkdir(filepath.Join(path, "hooks"), 0o755); err != nil && !os.IsExist(err) {
				return err
			}
		}
		return nil
	})
}

// rewriteConfig keeps only the allowlisted keys of a git config file.
func rewriteConfig(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	out := safeGitConfig(f)
	_ = f.Close()
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// safeGitConfig re-emits the allowlisted part of a git config in a
// canonical form: headers rebuilt from validated names, values that git
// would not unquote or unescape, URLs only on transports that connect.
// Anything it can't read plainly is dropped, never passed through.
func safeGitConfig(r io.Reader) string {
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
			section, header, wrote = parseHeader(line), "", false
			if section != "" {
				header = canonicalHeader(line, section)
			}
			if header == "" {
				section = ""
			}
			continue
		}
		if strings.HasSuffix(line, `\`) {
			skipping = true
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if section == "" || !keptConfig[section][key] || !plainValue.MatchString(val) {
			continue
		}
		if (key == "url" || key == "pushurl") && !safeURL.MatchString(val) {
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

// parseHeader returns the lowercased section name of a header line.
func parseHeader(line string) string {
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return ""
	}
	name := strings.TrimSpace(line[1:end])
	if i := strings.IndexAny(name, " \t\"."); i >= 0 {
		name = name[:i]
	}
	name = strings.ToLower(name)
	if _, ok := keptConfig[name]; !ok {
		return ""
	}
	return name
}

// canonicalHeader rebuilds a kept header: [core], [extensions], or
// [remote "name"] / [branch "name"] with a validated name. "" drops it.
func canonicalHeader(line, section string) string {
	switch section {
	case "core", "extensions":
		if strings.TrimSpace(strings.ToLower(line)) != "["+section+"]" {
			return ""
		}
		return "[" + section + "]"
	}
	rest := strings.TrimSpace(line[1+len(section):])
	if !strings.HasPrefix(rest, `"`) || !strings.HasSuffix(rest, `"]`) {
		return ""
	}
	sub := rest[1 : len(rest)-2]
	if !subsectionOK.MatchString(sub) {
		return ""
	}
	return "[" + section + ` "` + sub + `"]`
}
