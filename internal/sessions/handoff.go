/*
Handoff moves a live laptop session into the cluster: the working tree
(dirty state and .git included) and the Claude Code transcript travel over
the exec API into a session created to wait for them. The agent then starts
on the resume path and continues the laptop's conversation.
*/
package sessions

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PushTree streams dir (gzipped tar, laid down on the fly) into destDir in
// the pod, preserving the tree. destDir is created first.
func (s *Store) PushTree(ctx context.Context, pod, destDir, dir string, progress func(file string)) error {
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		tw := tar.NewWriter(gz)
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(dir, path)
			if rerr != nil || rel == "." {
				return rerr
			}
			hdr, herr := tar.FileInfoHeader(info, "")
			if herr != nil {
				return herr
			}
			hdr.Name = rel
			if info.Mode()&os.ModeSymlink != 0 {
				link, lerr := os.Readlink(path)
				if lerr != nil {
					return lerr
				}
				hdr.Linkname = link
			}
			if werr := tw.WriteHeader(hdr); werr != nil {
				return werr
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if progress != nil {
				progress(rel)
			}
			f, oerr := os.Open(path)
			if oerr != nil {
				return oerr
			}
			_, cerr := io.Copy(tw, f)
			if clerr := f.Close(); cerr == nil {
				cerr = clerr
			}
			return cerr
		})
		if err == nil {
			err = tw.Close()
		}
		if err == nil {
			err = gz.Close()
		} else {
			_ = tw.Close()
			_ = gz.Close()
		}
		_ = pw.CloseWithError(err)
	}()

	var stderr strings.Builder
	err := s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c", fmt.Sprintf("mkdir -p %q && tar -xzf - -C %q", destDir, destDir)},
		pr, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("push %s: %s", destDir, strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return nil
}

// MarkHandoffComplete releases the entrypoint's handoff gate.
func (s *Store) MarkHandoffComplete(ctx context.Context, pod string) error {
	var stderr strings.Builder
	err := s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c", "mkdir -p /workspace/.tiny && touch /workspace/.tiny/handoff-complete"},
		nil, io.Discard, &stderr)
	if err != nil {
		return fmt.Errorf("mark handoff complete: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return nil
}

// PullTree brings a session's directory back: the pod tars it out over the
// exec API and we lay it down under dest. Nothing is overwritten — dest
// must be empty or absent, so a pull can never eat local work.
func (s *Store) PullTree(ctx context.Context, pod, srcDir, dest string, progress func(file string)) (int, error) {
	if err := emptyOrAbsent(dest); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, err
	}

	pr, pw := io.Pipe()
	var stderr strings.Builder
	errc := make(chan error, 1)
	go func() {
		err := s.execStream(ctx, pod, agentContainer,
			[]string{"sh", "-c", fmt.Sprintf("cd %q 2>/dev/null && tar -czf - . || echo NODIR >&2", srcDir)},
			nil, pw, &stderr)
		_ = pw.CloseWithError(err)
		errc <- err
	}()

	count, err := untarInto(pr, dest, progress)
	if streamErr := <-errc; streamErr != nil {
		return count, fmt.Errorf("pull %s: %s", srcDir, strings.TrimSpace(stderr.String()+" "+streamErr.Error()))
	}
	if strings.Contains(stderr.String(), "NODIR") {
		return 0, fmt.Errorf("%s does not exist in the session", srcDir)
	}
	return count, err
}

// emptyOrAbsent refuses a destination that already holds anything.
func emptyOrAbsent(dest string) error {
	entries, err := os.ReadDir(dest)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty — pull into a new directory so nothing local is overwritten", dest)
	}
	return nil
}

// untarInto extracts a gzipped tar under dest. Entry names come from a pod
// whose files an AGENT writes, so every path is checked to land inside dest:
// a crafted name like ../../.ssh/authorized_keys must not escape.
func untarInto(r io.Reader, dest string, progress func(file string)) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("not a gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

	root, err := filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, err
		}
		target, err := safeJoin(root, hdr.Name)
		if err != nil {
			return count, err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&os.ModePerm|0o700); err != nil {
				return count, err
			}
		case tar.TypeSymlink:
			// An ABSOLUTE target must be refused outright: filepath.Join
			// absorbs the leading separator, so joining it tar-relative
			// ("/etc/passwd" under ".") yields "etc/passwd" and looks
			// contained. The link would then be followed by a later write.
			if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(hdr.Linkname, "/") {
				return count, fmt.Errorf("refusing symlink %s -> %s: absolute target", hdr.Name, hdr.Linkname)
			}
			// Resolve relative to where the link will LIVE, not to the tar root.
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), hdr.Linkname))
			if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
				return count, fmt.Errorf("refusing symlink %s -> %s: escapes the destination", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			// A directory component may itself have been planted as a
			// symlink by an earlier entry; O_NOFOLLOW only guards the last
			// one, so check the parent chain really sits inside dest.
			if err := parentInside(root, target); err != nil {
				return count, err
			}
			// Drop anything already there (a planted symlink) so the create
			// cannot be redirected through it.
			_ = os.Remove(target)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|oNoFollow, os.FileMode(hdr.Mode)&os.ModePerm|0o600)
			if err != nil {
				return count, err
			}
			// Bounded copy: a hostile tar must not fill the disk silently.
			if _, err := io.CopyN(f, tr, hdr.Size); err != nil && err != io.EOF {
				_ = f.Close()
				return count, err
			}
			if err := f.Close(); err != nil {
				return count, err
			}
			count++
			if progress != nil {
				progress(hdr.Name)
			}
		}
	}
}

// parentInside reports whether target's directory, with every symlink
// resolved, still lies within root — the guard against an earlier entry
// having redirected a directory component out of the tree.
func parentInside(root, target string) error {
	dir := filepath.Dir(target)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing created it yet; MkdirAll made it real
		}
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
		return fmt.Errorf("refusing %s: a parent directory leaves the destination", target)
	}
	return nil
}

// safeJoin resolves name under root and refuses anything that escapes it.
func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.Join(root, name))
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing path %q: escapes the destination", name)
	}
	return clean, nil
}
