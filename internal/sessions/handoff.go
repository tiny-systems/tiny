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
