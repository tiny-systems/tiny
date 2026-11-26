package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const kubeconfigTmpl = `apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster: {server: "https://%[1]s.example:6443"}
users:
- name: %[1]s
  user: {token: t}
contexts:
- name: %[1]s
  context: {cluster: %[1]s, user: %[1]s, namespace: ns-%[1]s}
current-context: %[1]s
`

func writeKubeconfig(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, fmt.Appendf(nil, kubeconfigTmpl, name), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// KUBECONFIG may list several files, as kubectl allows; a context from
// any of them must resolve.
func TestKubeconfigListIsMerged(t *testing.T) {
	a, b := writeKubeconfig(t, "alpha"), writeKubeconfig(t, "beta")
	t.Setenv("KUBECONFIG", a+string(os.PathListSeparator)+b)
	for _, ctx := range []string{"alpha", "beta"} {
		cfg, ns, err := loadConfig("", ctx)
		if err != nil {
			t.Fatalf("context %s: %v", ctx, err)
		}
		if cfg.Host != "https://"+ctx+".example:6443" || ns != "ns-"+ctx {
			t.Fatalf("context %s: host %s ns %s", ctx, cfg.Host, ns)
		}
	}
}
