package repo

import "testing"

const fixtureA = `
apiVersion: tiny/v2
modules:
  http-module:
    source: github.com/tiny-systems/http-module
    versions:
      - version: 1.4.2
        image: ghcr.io/tiny-systems/http-module:1.4.2
      - version: 1.4.1
        image: ghcr.io/tiny-systems/http-module:1.4.1
  common-module:
    versions:
      - version: 2.0.0
        image: ghcr.io/tiny-systems/common-module:2.0.0
`

// fixtureB also carries http-module, to exercise cross-repo ambiguity.
const fixtureB = `
apiVersion: tiny/v2
modules:
  http-module:
    versions:
      - version: 9.9.9
        image: ghcr.io/acme/http-module:9.9.9
`

func mustParse(t *testing.T, s string) *Index {
	t.Helper()
	idx, err := ParseIndex([]byte(s))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	return idx
}

func TestParseIndexRejectsWrongAPIVersion(t *testing.T) {
	if _, err := ParseIndex([]byte("apiVersion: tiny/v1\nmodules: {}\n")); err == nil {
		t.Fatal("expected error on unsupported apiVersion")
	}
}

func TestResolveSingleRepo(t *testing.T) {
	m := NewMerged([]string{"tinysystems"}, map[string]*Index{"tinysystems": mustParse(t, fixtureA)})

	// bare name → latest version (newest-first ⇒ [0])
	r, err := m.Resolve("http-module")
	if err != nil {
		t.Fatalf("resolve bare: %v", err)
	}
	if r.Version.Version != "1.4.2" || r.Repo != "tinysystems" {
		t.Fatalf("got %s@%s, want tinysystems/http-module@1.4.2", r.Repo, r.Version.Version)
	}

	// pinned version
	if r, _ := m.Resolve("http-module@1.4.1"); r == nil || r.Version.Version != "1.4.1" {
		t.Fatalf("pinned version resolve failed: %+v", r)
	}

	// missing version → error
	if _, err := m.Resolve("http-module@9.9.9"); err == nil {
		t.Fatal("expected error for missing version")
	}

	// legacy -v0 alias resolves to the clean name
	if r, err := m.Resolve("common-module-v0"); err != nil || r.Name != "common-module" {
		t.Fatalf("v0 alias failed: r=%+v err=%v", r, err)
	}

	// unknown module → error
	if _, err := m.Resolve("nope"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestResolveAmbiguityAcrossRepos(t *testing.T) {
	m := NewMerged(
		[]string{"tinysystems", "acme"},
		map[string]*Index{"tinysystems": mustParse(t, fixtureA), "acme": mustParse(t, fixtureB)},
	)

	// bare name in two repos → ambiguous
	if _, err := m.Resolve("http-module"); err == nil {
		t.Fatal("expected ambiguity error for http-module in two repos")
	}

	// qualifying by repo disambiguates
	r, err := m.Resolve("acme/http-module")
	if err != nil {
		t.Fatalf("qualified resolve: %v", err)
	}
	if r.Repo != "acme" || r.Version.Version != "9.9.9" {
		t.Fatalf("got %s@%s, want acme/http-module@9.9.9", r.Repo, r.Version.Version)
	}

	// a module in only one repo is still unambiguous
	if r, err := m.Resolve("common-module"); err != nil || r.Repo != "tinysystems" {
		t.Fatalf("unique module resolve failed: r=%+v err=%v", r, err)
	}
}
