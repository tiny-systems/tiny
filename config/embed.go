// Package config carries the runtime manifests inside the binary: the two
// CRDs and the namespace-scoped install. `tiny new` applies them on first
// contact, and the controller's own kustomize config is the same source —
// one repo, one copy, no drift.
package config

import "embed"

//go:embed crd/bases/*.yaml install/*.yaml
var Manifests embed.FS
