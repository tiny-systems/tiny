// Command tiny runs coding-agent sessions as Kubernetes workloads: start a
// session with a task, disconnect any time, answer when it needs you — your
// cluster, your keys, your repos.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tiny-systems/tiny/internal/cli"
)

// version is stamped at build time by goreleaser (-ldflags).
var version = "dev"

func installSelf(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.OpenFile(filepath.Join(dir, "tiny"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func main() {
	// `tiny --install-to <dir>` copies this very binary there and exits —
	// how the runner init container (distroless, no shell) seeds the CLI.
	if len(os.Args) == 3 && os.Args[1] == "--install-to" {
		if err := installSelf(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := cli.Execute(version); err != nil {
		os.Exit(1)
	}
}
