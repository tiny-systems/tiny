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
// `<module>-v<major>`. This is what lets v1 and v2 of a module live in one
// namespace (distinct releases, labels, node refs).
func ReleaseName(module string, major int) string {
	return fmt.Sprintf("%s-v%d", baseName(module), major)
}

// ReleaseName derives the release name for a resolved target from its module
// name + the version's SemVer major.
func (r *Resolved) ReleaseName() (string, error) {
	major, err := r.Version.Major()
	if err != nil {
		return "", err
	}
	return ReleaseName(r.Name, major), nil
}
