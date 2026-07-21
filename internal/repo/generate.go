package repo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// ManifestFile is the filename a module repo publishes to describe itself for
// the index generator. It is the transparent, git-visible source the generator
// aggregates (alongside — eventually — OCI image annotations).
const ManifestFile = "module.yaml"

// Manifest is one module repo's self-description (its `module.yaml`): the module
// name plus everything an index entry carries. The embedded Module fields
// (source, description, category, versions) are inlined into the file.
type Manifest struct {
	Name   string `json:"name"`
	Module `json:",inline"`
}

// ParseManifest decodes a module.yaml.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Name == "" {
		return nil, fmt.Errorf("manifest is missing `name`")
	}
	if len(m.Versions) == 0 {
		return nil, fmt.Errorf("module %q has no versions", m.Name)
	}
	return &m, nil
}

// BuildIndex assembles a tiny/v2 index from module manifests, sorting each
// module's versions newest-first (the invariant Resolve.latest relies on).
// Duplicate module names are an error — a module belongs to exactly one repo.
func BuildIndex(manifests []Manifest) (*Index, error) {
	idx := &Index{APIVersion: APIVersion, Modules: map[string]*Module{}}
	for i := range manifests {
		m := manifests[i]
		if _, dup := idx.Modules[m.Name]; dup {
			return nil, fmt.Errorf("duplicate module %q", m.Name)
		}
		mod := m.Module
		sortVersionsDesc(mod.Versions)
		idx.Modules[m.Name] = &mod
	}
	return idx, nil
}

// GenerateFromDir walks dir for module.yaml files and builds the index. One
// manifest per file; nested layout (dir/<module>/module.yaml) is fine.
func GenerateFromDir(dir string) (*Index, error) {
	var manifests []Manifest
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != ManifestFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		m, err := ParseManifest(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		// Inline a sibling values.yaml so authors keep the overlay in a real
		// file — applied to any version that doesn't carry its own inline block.
		if vals, err := os.ReadFile(filepath.Join(filepath.Dir(path), "values.yaml")); err == nil {
			for _, v := range m.Versions {
				if v.Values == "" {
					v.Values = string(vals)
				}
			}
		}
		manifests = append(manifests, *m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return nil, fmt.Errorf("no %s files found under %s", ManifestFile, dir)
	}
	return BuildIndex(manifests)
}

// MarshalIndex renders an index to YAML, stamping apiVersion and (when given) a
// generated timestamp. The timestamp is passed in rather than read from the
// clock so callers stay deterministic/testable.
func MarshalIndex(idx *Index, generated string) ([]byte, error) {
	idx.APIVersion = APIVersion
	idx.Generated = generated
	return yaml.Marshal(idx)
}

// sortVersionsDesc orders versions newest-first by SemVer (major, minor, patch).
func sortVersionsDesc(vs []*Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		return lessSemver(vs[j].Version, vs[i].Version) // j < i ⇒ i is newer ⇒ i first
	})
}

// lessSemver reports whether a < b comparing major.minor.patch (prerelease and
// build metadata are ignored for ordering — good enough for index sorting).
func lessSemver(a, b string) bool {
	a1, a2, a3 := semverParts(a)
	b1, b2, b3 := semverParts(b)
	if a1 != b1 {
		return a1 < b1
	}
	if a2 != b2 {
		return a2 < b2
	}
	return a3 < b3
}

func semverParts(v string) (maj, min, pat int) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	atoi := func(i int) int {
		if i < len(parts) {
			n, _ := strconv.Atoi(parts[i])
			return n
		}
		return 0
	}
	return atoi(0), atoi(1), atoi(2)
}
