package flow

import (
	"context"
	"encoding/json"
	_ "embed"
	"net/http"
	"time"

	platform "github.com/tiny-systems/platform-go"
)

//go:embed editor.html
var editorHTML []byte

// ServeEditor serves the browser editor on addr (e.g. "127.0.0.1:7775"): a
// small JSON API over the local cluster + the embedded single-page UI. This is
// the first live editor — a project picker, a flow (layer) switcher, and a
// canvas that renders the active flow's nodes and edges, polled from the
// cluster. The full gRPC-web FlowService (Serve) backs the richer canvas next.
func (s *Service) ServeEditor(ctx context.Context, addr string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		names, err := s.projectNames(r.Context())
		writeJSON(w, map[string]interface{}{"projects": names}, err)
	})
	mux.HandleFunc("/api/flows", func(w http.ResponseWriter, r *http.Request) {
		res, err := s.GetFlowList(r.Context(), &platform.GetFlowListRequest{ProjectName: r.URL.Query().Get("project")})
		names := []string{}
		if err == nil {
			for _, it := range res.Flows {
				names = append(names, it.Flow.GetResourceName())
			}
		}
		writeJSON(w, map[string]interface{}{"flows": names}, err)
	})
	mux.HandleFunc("/api/flow", func(w http.ResponseWriter, r *http.Request) {
		graph, err := s.flowGraph(r.Context(), r.URL.Query().Get("project"), r.URL.Query().Get("flow"))
		writeJSON(w, graph, err)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(editorHTML)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadTimeout: 15 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errCh:
		return err
	}
}

// projectNames lists the TinyProjects in the namespace for the picker.
func (s *Service) projectNames(ctx context.Context) ([]string, error) {
	mgr, err := s.manager()
	if err != nil {
		return nil, err
	}
	projects, err := mgr.GetProjectList(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names, nil
}

// flowGraph returns the active flow's nodes and edges as raw JSON — the same
// canvas shapes the gRPC stream emits, split by kind for the browser.
func (s *Service) flowGraph(ctx context.Context, project, flow string) (map[string][]json.RawMessage, error) {
	out := map[string][]json.RawMessage{"nodes": {}, "edges": {}}
	if project == "" || flow == "" {
		return out, nil
	}
	mgr, err := s.manager()
	if err != nil {
		return nil, err
	}
	events, _, err := s.buildFlowEvents(ctx, mgr, &platform.GetFlowStreamRequest{ProjectName: project, FlowName: flow})
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if len(e.Graph) == 0 {
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(e.Graph, &probe) != nil {
			continue
		}
		if _, isEdge := probe["source"]; isEdge {
			out["edges"] = append(out["edges"], e.Graph)
		} else {
			out["nodes"] = append(out["nodes"], e.Graph)
		}
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, v interface{}, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
