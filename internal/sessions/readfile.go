package sessions

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ReadFile returns a file from the session's agent container, read over
// the exec API with the caller's own access. The path goes to `head` as an
// argument, never through a shell. A file bigger than limit is refused
// rather than cut short: half an image is worse than none.
func (s *Store) ReadFile(ctx context.Context, pod, path string, limit int64) ([]byte, error) {
	if pod == "" {
		return nil, fmt.Errorf("the session has no running pod; resume it to read %s", path)
	}
	var out bytes.Buffer
	var stderr strings.Builder
	w := &capWriter{buf: &out, limit: limit}
	err := s.execStream(ctx, pod, agentContainer,
		[]string{"head", "-c", strconv.FormatInt(limit+1, 10), "--", path}, nil, w, &stderr)
	if w.over {
		return nil, fmt.Errorf("%s is larger than %d MB", path, limit>>20)
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("read %s: %s", path, strings.TrimPrefix(msg, "head: "))
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out.Bytes(), nil
}

// capWriter keeps at most limit bytes and notes when more arrived.
type capWriter struct {
	buf   *bytes.Buffer
	limit int64
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.limit - int64(w.buf.Len())
	if int64(len(p)) > room {
		w.over = true
		if room > 0 {
			w.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}
