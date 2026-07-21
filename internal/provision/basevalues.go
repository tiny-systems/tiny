package provision

import "strings"

// BaseValues builds the harness (operator-chart) values that every module
// install needs regardless of the module — the plumbing `moduleValues`
// constructs today, re-expressed as a nested values map derived from the
// install plan instead of the old catalog. The module's own values.yaml overlay
// (rbac / ingress / storage) is merged on top of this by the caller.
//
// `release` doubles as the operator's identity (`--name`): it is the
// coexistence-safe `<module>-v<major>` release name, so two majors of a module
// register as distinct operators in one namespace and their TinyNodes bind to
// the right one. (This replaces today's workspace-qualified
// `tinysystems/http-module-v0` identity.)
func BaseValues(image, release, version, namespace, natsURL string) map[string]any {
	repo, tag := splitImage(image)

	m := map[string]any{
		"fullnameOverride": release,
		"secrets":          map[string]any{"enabled": true},
		"controllerManager": map[string]any{
			"manager": map[string]any{
				"image": map[string]any{"repository": repo, "tag": tag},
				"args": []string{
					"run",
					"--grpc-server-bind-address=:8483",
					"--health-probe-bind-address=:8081",
					"--metrics-bind-address=127.0.0.1:8080",
					"--name=" + release,
					"--version=" + version,
					"--namespace=" + namespace,
				},
				"deleteArgs":  []string{"pre-delete", "--name=" + release, "--namespace=" + namespace},
				"installArgs": []string{"pre-install", "--name=" + release, "--namespace=" + namespace},
				"extraEnv": []map[string]any{
					{"name": "OTLP_DSN", "value": otelDSN},
					// Durable wire by default (JetStream WorkQueue: pod-death
					// survival + per-edge retry).
					{"name": "TINY_NATS_TRANSPORT", "value": "jetstream"},
				},
			},
		},
	}
	if natsURL != "" {
		m["natsURL"] = natsURL
	}
	return m
}

// splitImage splits an image ref into repository and tag. The tag is the segment
// after the last ':' only when it isn't part of a registry host:port (i.e. no
// '/' follows it). "ghcr.io/x/http-module:2.3.1" → ("ghcr.io/x/http-module",
// "2.3.1"); "localhost:5000/x" → ("localhost:5000/x", "").
func splitImage(ref string) (repository, tag string) {
	if i := strings.LastIndexByte(ref, ':'); i >= 0 && !strings.ContainsRune(ref[i+1:], '/') {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
