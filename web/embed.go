// Package web embeds the built flow-editor SPA (a thin host around
// @tinysystems/editor) and serves it as an http.Handler. The tiny CLI hands
// this to flow.Serve as the static fallback for any non-gRPC-web request, so
// the editor and its FlowService backend share one localhost origin.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// dist is produced by `pnpm build` in this directory (Vite). It is committed
// so `go build`/`go install` work without a Node toolchain; the release
// workflow rebuilds it first. The .gitkeep keeps the embed valid before the
// first build.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the SPA: static assets by path, with an index.html fallback
// so a reload on any route still boots the app.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the file if it exists; otherwise fall back to index.html
		// (single-page app — the server owns only "/", assets, and gRPC-web).
		if r.URL.Path != "/" {
			if _, statErr := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); statErr == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	}), nil
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
