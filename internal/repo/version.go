package repo

import (
	"fmt"
	"strconv"
	"strings"
)

// Major parses the SemVer major from a version string ("2.3.1" → 2, "v0.5.24"
// → 0, "1.0.0-rc1" → 1). The major is the coexistence coordinate: two majors of
// a module run side by side in the same namespace, so it drives the release
// name and every K8s resource identity. See docs/design §7.
func Major(version string) (int, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" {
		return 0, fmt.Errorf("empty version")
	}
	head := v
	if i := strings.IndexByte(head, '.'); i >= 0 {
		head = head[:i] // major is everything before the first dot
	}
	if i := strings.IndexByte(head, '-'); i >= 0 {
		head = head[:i] // tolerate a suffix-only version like "2-rc1"
	}
	n, err := strconv.Atoi(head)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid semver major in %q", version)
	}
	return n, nil
}

// Major returns the version's SemVer major.
func (v *Version) Major() (int, error) { return Major(v.Version) }

// baseName strips a trailing legacy major suffix (`-v<digits>`) from a module
// name so we don't double-suffix. `http-module-v0` → `http-module`; a plain
// name is returned unchanged.
func baseName(name string) string {
	if i := strings.LastIndex(name, "-v"); i >= 0 {
		if _, err := strconv.Atoi(name[i+2:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// ReleaseName is the coexistence-safe helm release / resource identity:
// `<repo>-<module>-v<major>`. Two coordinates are derived at install time, so
// both dimensions can coexist in one namespace (distinct releases, labels,
// node refs):
//
//   - major   — v1 and v2 of the same module (design §7)
//   - repo    — the same module name published by different people (§7.1).
//     A cluster holds modules from many publishers and nothing stops two of
//     them shipping "http-module"; without this the second install silently
//     replaces the first.
//
// repo is the LOCAL repo name from repos.yaml, not the GitHub org — identity
// stays under the cluster operator's control and survives an upstream rename.
// It may contain dashes (tiny-systems does); this string is an opaque identity
// and is never split back into its parts. Two pairs can therefore generate one
// name ("a-b"+"c" and "a"+"b-c"), which the installer catches by comparing the
// authoritative repo/module labels rather than by restricting names.
//
// An empty repo yields the legacy `<module>-v<major>` form, so callers that
// don't know the publisher keep working.
func ReleaseName(repo, module string, major int) string {
	name := baseName(module)
	if repo != "" {
		name = repo + "-" + name
	}
	return fmt.Sprintf("%s-v%d", name, major)
}

// ReleaseName derives the release name for a resolved target from its repo +
// module name + the version's SemVer major.
func (r *Resolved) ReleaseName() (string, error) {
	major, err := r.Version.Major()
	if err != nil {
		return "", err
	}
	return ReleaseName(r.Repo, r.Name, major), nil
}
