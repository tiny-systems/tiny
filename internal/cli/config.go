package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// pinnedConfig is tiny's one piece of local state: which cluster this
// machine's tiny talks to. Pinned on first contact, shown everywhere,
// changed only by editing the file — one tiny, one cluster, no
// wrong-context surprises.
type pinnedConfig struct {
	Context   string `json:"context"`
	Namespace string `json:"namespace,omitempty"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tiny", "config.json"), nil
}

func loadPinned() *pinnedConfig {
	path, err := configPath()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	c := &pinnedConfig{}
	if json.Unmarshal(raw, c) != nil || c.Context == "" {
		return nil
	}
	return c
}

func savePinned(c *pinnedConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// resolveTarget decides the context+namespace every command acts on and pins
// the choice on first use. Precedence: explicit flags, then the pinned
// config, then the kubeconfig's current-context (which then gets pinned).
func resolveTarget() (ctxName, namespace string, err error) {
	// Inside a pod (a runner job) the mounted serviceaccount IS the target:
	// no kubeconfig, no picker, the pod's own namespace.
	if _, statErr := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); statErr == nil {
		ns := flagNamespace
		if ns == "" {
			if raw, readErr := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); readErr == nil {
				ns = strings.TrimSpace(string(raw))
			}
		}
		return "", ns, nil
	}
	pinned := loadPinned()

	switch {
	case flagContext != "":
		ctxName = flagContext
	case pinned != nil:
		ctxName = pinned.Context
	default:
		ctxName = currentKubeContext()
	}
	if ctxName == "" {
		return "", "", fmt.Errorf("no kubeconfig context found — is kubectl configured? (set one with --context)")
	}

	switch {
	case flagNamespace != "":
		namespace = flagNamespace
	case pinned != nil && pinned.Namespace != "":
		namespace = pinned.Namespace
	default:
		namespace = defaultNamespace
	}

	// First contact pins the target for every future run — and pinning is a
	// CHOICE the human makes from a list, not a side effect. An explicit
	// --context flag (or --yes) is that choice already; otherwise the
	// kubeconfig's contexts are offered and one is picked.
	if pinned == nil {
		if flagContext == "" && !flagYes {
			picked, ns, err := pickTarget(ctxName, namespace)
			if err != nil {
				return "", "", err
			}
			ctxName, namespace = picked, ns
		}
		if err := savePinned(&pinnedConfig{Context: ctxName, Namespace: namespace}); err == nil {
			path, _ := configPath()
			fmt.Printf("  ✓ target pinned: %s/%s  (change by editing %s)\n", ctxName, namespace, path)
		}
	}
	return ctxName, namespace, nil
}

// kubeContexts lists context names from the kubeconfig, sorted — read the
// way kubectl reads it (KUBECONFIG, merges included), without spawning a
// kubectl child for it.
func kubeContexts() ([]string, error) {
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func currentKubeContext() string {
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return ""
	}
	return raw.CurrentContext
}

// readLine reads one trimmed line from stdin — the single prompt reader,
// so every y/N question behaves the same (Scanln stopped at first space).
func readLine() string {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// confirmed reads one line and reports whether the human said yes.
func confirmed(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// pickEveryStart runs the target picker at fleet-screen launch — which
// cluster and which group of agents you look at is a per-start choice,
// with the last one preselected so enter-enter repeats it. Flags (or a
// non-TTY stdin) skip the ceremony.
func pickEveryStart() error {
	if flagContext != "" || flagYes {
		return nil
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		return nil // piped/scripted: use the stored target
	}
	current := currentKubeContext()
	defNS := defaultNamespace
	if pinned := loadPinned(); pinned != nil {
		current = pinned.Context
		if pinned.Namespace != "" {
			defNS = pinned.Namespace
		}
	}
	ctxName, ns, err := pickTarget(current, defNS)
	if err != nil {
		return err
	}
	return savePinned(&pinnedConfig{Context: ctxName, Namespace: ns})
}
