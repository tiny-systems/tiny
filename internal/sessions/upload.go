/*
Uploads stream a tar of one file over the exec API — kubectl cp exposes no
progress, and a multi-hundred-MB drop deserves a percent. The progress
callback fires on the LOCAL read side; display is the caller's business
(the fleet status line, or tmux display-message inside the session).
*/
package sessions

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// streamUpload puts localPath into pod:/workspace/uploads/ via tar-over-exec,
// reporting copied bytes as it goes. Returns the remote path.
func (s *Store) streamUpload(ctx context.Context, pod, container, localPath string, progress func(done, total int64)) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	base := filepath.Base(localPath)
	remote := "/workspace/uploads/" + base

	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		hdr := &tar.Header{Name: base, Mode: 0o644, Size: st.Size()}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		src := io.Reader(f)
		if progress != nil {
			src = &progressReader{r: f, total: st.Size(), report: progress}
		}
		if _, err := io.Copy(tw, src); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(tw.Close())
	}()

	var stderr bytes.Buffer
	err = s.execStream(ctx, pod, container,
		[]string{"sh", "-c", "mkdir -p /workspace/uploads && tar -xf - -C /workspace/uploads"},
		pr, io.Discard, &stderr)
	if err != nil {
		return "", fmt.Errorf("upload: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return remote, nil
}

type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	p.report(p.done, p.total)
	return n, err
}

// execStream runs one command in the pod, wiring the given streams.
func (s *Store) execStream(ctx context.Context, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg := setGroupVersion(s.Kube.RESTConfig)
	restClient, err := restClientFor(cfg)
	if err != nil {
		return err
	}
	req := restClient.Post().
		Resource("pods").Name(pod).Namespace(s.Kube.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdin:     stdin != nil, Stdout: stdout != nil, Stderr: stderr != nil,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr})
}

// notifyPane flashes a short line over the session's tmux status bar —
// upload progress a human inside the session can see.
func (s *Store) notifyPane(ctx context.Context, pod, text string) {
	_ = s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c",
			`exec "$([ -x /tiny/bin/tmux ] && echo /tiny/bin/tmux || echo tmux)" display-message -d 1500 -- "$0"`, text},
		nil, io.Discard, io.Discard)
}
