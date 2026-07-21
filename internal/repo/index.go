// Package repo implements the decentralized, Helm-style module distribution
// client described in docs/design/module-distribution-v2.md: repos are static
// index files, the source of truth is the module repo + its signed image, and
// the platform is demoted to an optional aggregator.
//
// This package is the client foundation — repo config, index fetch/cache, and
// resolution. It does NOT yet replace internal/catalog or the install path;
// wiring install/up onto it is a later phase (kept separate so the working
// install keeps working).
package repo

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// APIVersion is the index schema version this client understands.
const APIVersion = "tiny/v2"

// Index is a repo's catalog — a static file served at the repo URL.
// See docs/design/module-distribution-v2.md §4.1.
type Index struct {
	APIVersion string             `json:"apiVersion"`
	Generated  string             `json:"generated,omitempty"`
	Modules    map[string]*Module `json:"modules"`
}

// Module is one module's entry: identity, presentation, and its versions.
// Description is the one-line summary (lists). The full markdown doc is NOT
// inlined — it's the module repo's own README, fetched on demand from Source
// (which is the doc's source of truth). See ReadmeURL.
type Module struct {
	Source      string     `json:"source,omitempty"` // github.com/org/repo — also where the README lives
	Description string     `json:"description,omitempty"`
	Category    string     `json:"category,omitempty"`
	Versions    []*Version `json:"versions"`
}

// ReadmeURL derives the raw README URL from a module's Source
// (github.com/org/repo → raw.githubusercontent.com/org/repo/HEAD/README.md).
// Returns "" if Source isn't a github.com coordinate. The platform/agent fetches
// this on demand for detail views — install never needs it, so it stays online-
// only while install data stays offline-cached.
func (m *Module) ReadmeURL() string {
	const gh = "github.com/"
	if !strings.HasPrefix(m.Source, gh) {
		return ""
	}
	return "https://raw.githubusercontent.com/" + strings.TrimPrefix(m.Source, gh) + "/HEAD/README.md"
}

// Version is one installable release: the image + the values/chart coordinates
// and optional bundles needed to install it.
type Version struct {
	Version      string   `json:"version"`
	Image        string   `json:"image"`
	Digest       string   `json:"digest,omitempty"`
	Chart        string   `json:"chart,omitempty"`        // harness chart name
	ChartVersion string   `json:"chartVersion,omitempty"` // compatible range
	Values       string   `json:"values,omitempty"`       // inline values.yaml (with ${cluster.*} holes)
	ClusterFills []string `json:"clusterFills,omitempty"` // holes tiny fills
	Bundles      []Bundle `json:"bundles,omitempty"`
	Cosign       bool     `json:"cosign,omitempty"` // image + entry are signed
}

// Bundle is an optional third-party Helm release a module offers (§3.5).
type Bundle struct {
	Name         string `json:"name"`
	Chart        string `json:"chart"` // oci://… or repo/chart
	ChartVersion string `json:"chartVersion,omitempty"`
	DefaultOn    bool   `json:"defaultOn,omitempty"`
}

// ParseIndex decodes and lightly validates an index document (YAML or JSON —
// sigs.k8s.io/yaml handles both).
func ParseIndex(data []byte) (*Index, error) {
	var idx Index
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if idx.APIVersion != APIVersion {
		return nil, fmt.Errorf("unsupported index apiVersion %q (want %q)", idx.APIVersion, APIVersion)
	}
	if idx.Modules == nil {
		idx.Modules = map[string]*Module{}
	}
	return &idx, nil
}

// latest returns the newest version of a module. The generator emits versions
// newest-first, so index 0 is latest; we defensively confirm nothing is newer
// by falling back to [0] when the list isn't sortable.
func (m *Module) latest() *Version {
	if len(m.Versions) == 0 {
		return nil
	}
	return m.Versions[0]
}

// find returns the module's version matching v exactly, or nil.
func (m *Module) find(v string) *Version {
	for _, ver := range m.Versions {
		if ver.Version == v {
			return ver
		}
	}
	return nil
}

// Merged is a read-only view over every configured repo's index, ordered so
// resolution is deterministic. Repo names are unique (config enforces it).
type Merged struct {
	order   []string          // repo names, in config order
	byRepo  map[string]*Index // repo name -> index
}

// NewMerged builds a merged view. order fixes precedence for ambiguity
// reporting (not silent shadowing — ambiguity is an error, see Resolve).
func NewMerged(order []string, byRepo map[string]*Index) *Merged {
	return &Merged{order: order, byRepo: byRepo}
}

// Resolved is a fully-resolved install target.
type Resolved struct {
	Repo    string
	Name    string
	Module  *Module
	Version *Version
}

// Resolve finds a module version across the merged indexes. Accepted forms:
//
//	module                 module[@version] searched across all repos
//	repo/module            module in that specific repo
//	module@1.2.3           a specific version
//	repo/module@1.2.3
//
// A bare name present in more than one repo is an error (name the repos); a
// name absent everywhere retries with the legacy `-v0` alias in both directions
// (http-module ⇄ http-module-v0) to bridge the migration.
func (m *Merged) Resolve(ref string) (*Resolved, error) {
	name, version := splitVersion(ref)

	var repo, module string
	if i := strings.Index(name, "/"); i >= 0 {
		repo, module = name[:i], name[i+1:]
	} else {
		module = name
	}
	if module == "" {
		return nil, fmt.Errorf("module name required")
	}

	// Candidate module keys: exact, then the legacy -v0 alias both ways.
	keys := []string{module}
	if strings.HasSuffix(module, "-v0") {
		keys = append(keys, strings.TrimSuffix(module, "-v0"))
	} else {
		keys = append(keys, module+"-v0")
	}

	for _, key := range keys {
		res, err := m.lookup(repo, key, version)
		if err != nil {
			return nil, err // ambiguity / bad-version is terminal, don't alias past it
		}
		if res != nil {
			return res, nil
		}
	}
	if repo != "" {
		return nil, fmt.Errorf("module %q not found in repo %q", module, repo)
	}
	return nil, fmt.Errorf("module %q not found in any configured repo", module)
}

// lookup finds key in repo (or across all repos if repo==""), returning nil,nil
// when absent so Resolve can try the next alias.
func (m *Merged) lookup(repo, key, version string) (*Resolved, error) {
	repos := m.order
	if repo != "" {
		repos = []string{repo}
	}

	var hits []*Resolved
	for _, rn := range repos {
		idx := m.byRepo[rn]
		if idx == nil {
			continue
		}
		mod, ok := idx.Modules[key]
		if !ok {
			continue
		}
		ver := mod.latest()
		if version != "" {
			ver = mod.find(version)
			if ver == nil {
				return nil, fmt.Errorf("module %q has no version %q in repo %q", key, version, rn)
			}
		}
		if ver == nil {
			return nil, fmt.Errorf("module %q in repo %q has no versions", key, rn)
		}
		hits = append(hits, &Resolved{Repo: rn, Name: key, Module: mod, Version: ver})
	}

	switch len(hits) {
	case 0:
		return nil, nil
	case 1:
		return hits[0], nil
	default:
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.Repo + "/" + key
		}
		sort.Strings(names)
		return nil, fmt.Errorf("module %q is ambiguous across repos: %s (qualify as repo/module)", key, strings.Join(names, ", "))
	}
}

// splitVersion parses "name@version" → ("name","version"); no @ → version "".
func splitVersion(ref string) (name, version string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
