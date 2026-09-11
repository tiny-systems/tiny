/*
Package webui is the read-only fleet page: what every session is doing,
what each one has changed, and which files two sessions are both editing.

Read-only on purpose. Answering a question or messaging a session would
run as this server's ServiceAccount, and tiny's gate means the human's own
credentials perform approved actions — a browser button would quietly
break that. The page watches; the CLI acts.

It is an add-on, so an idle namespace still runs nothing of ours.
*/
package webui

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/tiny-systems/tiny/internal/sessions"
)

//go:embed page.html
var pageFS embed.FS

// Server renders the fleet from a Store. Footprints are read over the exec
// API, which is far slower than listing CRDs, so they are cached briefly
// and refreshed in the background rather than on every page load.
type Server struct {
	Store *sessions.Store
	// FootprintTTL is how stale a cached footprint may be. Zero means 30s.
	FootprintTTL time.Duration

	tpl *template.Template

	mu    sync.Mutex
	cache map[string]cachedFootprint
}

type cachedFootprint struct {
	f   sessions.Footprint
	at  time.Time
	pod string
}

// row is one line of the page: the session plus what it has changed.
type row struct {
	sessions.Row
	Footprint sessions.Footprint
	Collides  []string // files this session shares with another
	Age       string
}

type pageData struct {
	Target    string
	Rows      []row
	Overlap   []overlapEntry
	Questions []questionEntry
	Refreshed string
	Err       string
}

type overlapEntry struct {
	Path     string
	Sessions []string
}

type questionEntry struct {
	Name    string
	Session string
	Text    string
	Answer  string
}

// Handler serves the page and its health endpoint.
func (s *Server) Handler() (http.Handler, error) {
	tpl, err := template.New("page.html").ParseFS(pageFS, "page.html")
	if err != nil {
		return nil, err
	}
	s.tpl = tpl

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", s.serveFleet)
	return mux, nil
}

func (s *Server) serveFleet(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	data := pageData{
		Target:    s.Store.Target(),
		Refreshed: time.Now().Format("15:04:05"),
	}
	snap, err := s.Store.Load(ctx)
	if err != nil {
		data.Err = err.Error()
		s.render(w, data)
		return
	}

	prints := map[string]sessions.Footprint{}
	for _, sr := range snap.Rows {
		f := s.footprint(ctx, sr.Name, sr.Pod)
		if sr.Pod != "" {
			prints[sr.Name] = f
		}
		data.Rows = append(data.Rows, row{Row: sr, Footprint: f, Age: shortAge(sr.Age)})
		if q := sr.Question; q != nil {
			data.Questions = append(data.Questions, questionEntry{
				Name: q.Name, Session: sr.Name, Text: q.Spec.Text,
				Answer: fmt.Sprintf("tiny answer %s <your answer>", q.Name),
			})
		}
	}

	over := sessions.Overlap(prints)
	paths := make([]string, 0, len(over))
	for p := range over {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	collides := map[string]map[string]bool{}
	for _, p := range paths {
		names := over[p]
		slices.Sort(names)
		data.Overlap = append(data.Overlap, overlapEntry{Path: p, Sessions: names})
		for _, n := range names {
			if collides[n] == nil {
				collides[n] = map[string]bool{}
			}
			collides[n][p] = true
		}
	}
	for i := range data.Rows {
		for p := range collides[data.Rows[i].Name] {
			data.Rows[i].Collides = append(data.Rows[i].Collides, p)
		}
		slices.Sort(data.Rows[i].Collides)
	}

	s.render(w, data)
}

func (s *Server) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// footprint returns a cached reading, refreshing when stale. A session with
// no pod has nothing to read.
func (s *Server) footprint(ctx context.Context, name, pod string) sessions.Footprint {
	if pod == "" {
		return sessions.Footprint{Err: "no pod"}
	}
	ttl := s.FootprintTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cachedFootprint{}
	}
	hit, ok := s.cache[name]
	s.mu.Unlock()
	if ok && hit.pod == pod && time.Since(hit.at) < ttl {
		return hit.f
	}

	f := s.Store.Footprint(ctx, pod)
	s.mu.Lock()
	s.cache[name] = cachedFootprint{f: f, at: time.Now(), pod: pod}
	s.mu.Unlock()
	return f
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0fm", d.Minutes())
	case d < 24*time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	default:
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	}
}
