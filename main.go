// Command tiny is the local front door to the Tiny Systems agent
// runtime: install it onto your own Kubernetes cluster, drive it by
// prompt from your editor, and watch agents assemble and run as real
// workloads — your cluster, your keys, your data.
package main

import (
	"os"

	"github.com/tiny-systems/tiny/cmd"
)

// version is stamped at build time by goreleaser (-ldflags).
var version = "dev"

func main() {
	if err := cmd.Execute(version); err != nil {
		os.Exit(1)
	}
}
