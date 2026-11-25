// Command krewmanifest renders .krew.yaml for one release, with each
// archive's real sha256 taken from goreleaser's checksums.txt. The release
// workflow attaches the result to the release as tiny-krew.yaml, which
// `kubectl krew install --manifest-url=...` installs from, and which is
// the manifest submitted to krew-index.
//
//	go run ./hack/krewmanifest -tag v0.16.0 -checksums dist/checksums.txt > tiny-krew.yaml
//
// Rendering follows krew-release-bot, which updates krew-index from the
// same template: the outer template sees .TagName, and addURIAndSha
// renders its URL argument as a template of its own.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"text/template"
)

func main() {
	tag := flag.String("tag", "", "release tag, e.g. v0.16.0")
	sums := flag.String("checksums", "dist/checksums.txt", "goreleaser checksums file")
	tmpl := flag.String("template", ".krew.yaml", "krew manifest template")
	flag.Parse()
	if err := run(*tag, *sums, *tmpl, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "krewmanifest:", err)
		os.Exit(1)
	}
}

func run(tag, sumsPath, tmplPath string, w io.Writer) error {
	if !strings.HasPrefix(tag, "v") {
		return fmt.Errorf("-tag must look like v1.2.3, got %q", tag)
	}
	raw, err := os.ReadFile(tmplPath)
	if err != nil {
		return err
	}
	f, err := os.Open(sumsPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sums, err := parseChecksums(f)
	if err != nil {
		return err
	}
	out, err := Render(raw, tag, func(uri string) (string, error) {
		sum, ok := sums[path.Base(uri)]
		if !ok {
			return "", fmt.Errorf("%s is not in %s", path.Base(uri), sumsPath)
		}
		return sum, nil
	})
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

// Render fills in the template for tag. sha256 returns an archive's
// checksum given its download URL.
func Render(tmpl []byte, tag string, sha256 func(uri string) (string, error)) ([]byte, error) {
	funcs := template.FuncMap{
		"addURIAndSha": func(url, tag string) (string, error) {
			inner, err := template.New("url").Parse(url)
			if err != nil {
				return "", err
			}
			var b bytes.Buffer
			if err := inner.Execute(&b, struct{ TagName string }{tag}); err != nil {
				return "", err
			}
			sum, err := sha256(b.String())
			if err != nil {
				return "", err
			}
			// Quoted, so a checksum that happens to be all digits stays a
			// string in YAML.
			return fmt.Sprintf("uri: %s\n    sha256: %q", b.String(), sum), nil
		},
	}
	t, err := template.New("krew").Funcs(funcs).Option("missingkey=error").Parse(string(tmpl))
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, struct{ TagName string }{tag}); err != nil {
		return nil, fmt.Errorf("render template: %w", err)
	}
	return out.Bytes(), nil
}

// parseChecksums reads "<sha256>  <file>" lines.
func parseChecksums(r io.Reader) (map[string]string, error) {
	sums := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("unexpected checksums line %q", sc.Text())
		}
		sums[fields[1]] = fields[0]
	}
	return sums, sc.Err()
}
