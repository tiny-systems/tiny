/*
Attach is tiny's own terminal pipe into a session — client-go exec instead
of shelling to kubectl, because owning the byte stream buys the flagship
trick: a file dragged onto the attached window arrives as a bracketed
paste of a local path, and by the time it reaches claude's prompt it has
become a real file in the workspace and the path claude sees is the
remote one.
*/
package sessions

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// Attach connects this terminal to the session's tmux. Blocks until detach.
func (s *Store) Attach(ctx context.Context, session, pod string) error {
	cfg := s.Kube.RESTConfig
	restClient, err := restClientFor(setGroupVersion(cfg))
	if err != nil {
		return err
	}
	req := restClient.Post().
		Resource("pods").Name(pod).Namespace(s.Kube.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: agentContainer,
			// The injected payload's tmux first: exec'd processes get the
			// image's default PATH, not the entrypoint's.
			Command: []string{"sh", "-c",
				`exec "$([ -x /tiny/bin/tmux ] && echo /tiny/bin/tmux || echo tmux)" -u attach -t main`},
			Stdin: true, Stdout: true, Stderr: false, TTY: true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return err
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer func() { _ = term.Restore(fd, oldState) }()

	sizes := make(chan remotecommand.TerminalSize, 1)
	winch := make(chan os.Signal, 1)
	stopWinch := notifyResize(winch)
	defer stopWinch()
	go func() {
		push := func() {
			if w, h, err := term.GetSize(fd); err == nil {
				select {
				case sizes <- remotecommand.TerminalSize{Width: uint16(w), Height: uint16(h)}:
				default:
				}
			}
		}
		push()
		for range winch {
			push()
		}
	}()

	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             &pasteInterceptor{src: os.Stdin, store: s, session: session, pod: pod, ctx: ctx},
		Stdout:            os.Stdout,
		Tty:               true,
		TerminalSizeQueue: sizeQueue(sizes),
	})
}

type sizeQueue chan remotecommand.TerminalSize

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	s, ok := <-q
	if !ok {
		return nil
	}
	return &s
}

func restClientFor(cfg *rest.Config) (rest.Interface, error) {
	return rest.RESTClientFor(cfg)
}

func setGroupVersion(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &corev1.SchemeGroupVersion
	c.APIPath = "/api"
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return c
}

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// pasteInterceptor passes terminal input through untouched — except a
// bracketed paste whose entire content is an existing local file path.
// That one becomes an upload, and the paste claude receives is the
// workspace path of the uploaded file.
type pasteInterceptor struct {
	src     *os.File
	store   *Store
	session string
	pod     string
	ctx     context.Context

	buf     []byte // pending output
	paste   []byte // accumulating paste content
	inPaste bool
	tail    []byte // partial escape-sequence tail between reads
}

func (p *pasteInterceptor) Read(out []byte) (int, error) {
	for len(p.buf) == 0 {
		chunk := make([]byte, 4096)
		n, err := p.src.Read(chunk)
		if n > 0 {
			p.consume(chunk[:n])
		}
		if err != nil {
			if len(p.buf) > 0 {
				break
			}
			return 0, err
		}
	}
	n := copy(out, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *pasteInterceptor) consume(in []byte) {
	data := append(p.tail, in...)
	p.tail = nil
	for len(data) > 0 {
		if p.inPaste {
			if i := bytes.Index(data, pasteEnd); i >= 0 {
				p.paste = append(p.paste, data[:i]...)
				data = data[i+len(pasteEnd):]
				p.inPaste = false
				p.buf = append(p.buf, p.finishPaste()...)
				continue
			}
			keep := partialSuffix(data, pasteEnd)
			p.paste = append(p.paste, data[:len(data)-keep]...)
			p.tail = append([]byte{}, data[len(data)-keep:]...)
			// A paste that never terminates must not buffer forever.
			if len(p.paste) > 1<<20 {
				p.buf = append(p.buf, pasteStart...)
				p.buf = append(p.buf, p.paste...)
				p.paste, p.inPaste = nil, false
			}
			return
		}
		if i := bytes.Index(data, pasteStart); i >= 0 {
			p.buf = append(p.buf, data[:i]...)
			data = data[i+len(pasteStart):]
			p.inPaste = true
			p.paste = nil
			continue
		}
		keep := partialSuffix(data, pasteStart)
		p.buf = append(p.buf, data[:len(data)-keep]...)
		p.tail = append([]byte{}, data[len(data)-keep:]...)
		return
	}
}

// finishPaste turns a completed paste back into bytes for the wire. A
// paste that is a dropped local file is swallowed entirely: the upload
// runs in the background with progress flashed over the tmux bar, and the
// workspace path arrives in claude's prompt through the inbox when the
// bytes are really there — a big drop must not freeze the terminal.
func (p *pasteInterceptor) finishPaste() []byte {
	content := p.paste
	p.paste = nil
	if path, ok := droppedPath(string(content)); ok {
		go p.uploadInBackground(path)
		return nil
	}
	out := make([]byte, 0, len(content)+12)
	out = append(out, pasteStart...)
	out = append(out, content...)
	out = append(out, pasteEnd...)
	return out
}

func (p *pasteInterceptor) uploadInBackground(path string) {
	// Detach must not kill an upload in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	name := filepath.Base(path)
	p.store.notifyPane(ctx, p.pod, "⇪ uploading "+name+"…")
	lastPct := -10
	remote, err := p.store.uploadFile(ctx, p.session, path, true, func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(done * 100 / total)
		if pct >= lastPct+10 {
			lastPct = pct
			p.store.notifyPane(ctx, p.pod, fmt.Sprintf("⇪ %s %d%%", name, pct))
		}
	})
	if err != nil {
		p.store.notifyPane(ctx, p.pod, "⇪ upload failed: "+name)
		return
	}
	p.store.notifyPane(ctx, p.pod, "⇪ done: "+remote)
}

// partialSuffix reports how many trailing bytes of data could be the
// beginning of marker and must wait for the next read.
func partialSuffix(data, marker []byte) int {
	for l := min(len(marker)-1, len(data)); l > 0; l-- {
		if bytes.Equal(data[len(data)-l:], marker[:l]) {
			return l
		}
	}
	return 0
}
