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
	// Profiles are named targets: `tiny -p work` instead of remembering
	// which GKE string is which. The pin stays the no-flag default.
	Profiles map[string]profileTarget `json:"profiles,omitempty"`
	// LastProfile is what the start picker preselects — enter-enter
	// repeats yesterday's choice.
	LastProfile string `json:"lastProfile,omitempty"`
}

type profileTarget struct {
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
	if json.Unmarshal(raw, c) != nil || (c.Context == "" && len(c.Profiles) == 0) {
		return nil
	}
	return c
}

// saveConfig writes the whole config (pin and profiles) back.
func saveConfig(c *pinnedConfig) error { return savePinned(c) }

func savePinned(c *pinnedConfig) error {
	// Merge, don't clobber: a caller re-pinning the default must not wipe
	// the profiles (or the last-used marker) another path saved.
	if existing := loadPinned(); existing != nil {
		if c.Profiles == nil {
			c.Profiles = existing.Profiles
		}
		if c.LastProfile == "" {
			c.LastProfile = existing.LastProfile
		}
	}
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

	// An explicit --context is the loudest possible intent; a profile
	// name never overrides it.
	if flagProfile != "" && flagContext == "" {
		if pinned == nil || pinned.Profiles[flagProfile].Context == "" {
			return "", "", fmt.Errorf("no profile %q — create it: tiny profile save %s", flagProfile, flagProfile)
		}
		p := pinned.Profiles[flagProfile]
		ns := p.Namespace
		if flagNamespace != "" {
			ns = flagNamespace
		}
		if ns == "" {
			ns = defaultNamespace
		}
		return p.Context, ns, nil
	}

	switch {
	case flagContext != "":
		ctxName = flagContext
	case pinned != nil && pinned.Context != "":
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
	if pinned == nil || pinned.Context == "" {
		if flagContext == "" && !flagYes {
			picked, ns, err := pickTarget(ctxName, namespace)
			if err != nil {
				return "", "", err
			}
			ctxName, namespace = picked, ns
		}
		toSave := &pinnedConfig{Context: ctxName, Namespace: namespace}
		if pinned != nil {
			toSave.Profiles = pinned.Profiles
		}
		if err := savePinned(toSave); err == nil {
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
// stdinIsTTY reports whether a human is on the other end of stdin.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func pickEveryStart() error {
	if flagContext != "" || flagProfile != "" || flagYes {
		return nil
	}
	if !stdinIsTTY() {
		return nil // piped/scripted: use the stored target
	}
	pinned := loadPinned()

	// With saved profiles the picker speaks their language: work, home,
	// enter-enter repeats yesterday's. "other…" falls back to the raw
	// context/namespace walk for the cluster that has no name yet.
	if pinned != nil && len(pinned.Profiles) > 0 {
		choice, err := pickProfile(pinned)
		if err != nil {
			return err
		}
		if choice != "" {
			flagProfile = choice
			pinned.LastProfile = choice
			return savePinned(pinned)
		}
		// "＋ create a new profile…": the full journey, then use it now.
		ctxName, ns, name, err := pickAndOfferProfile()
		if err != nil {
			return err
		}
		if name != "" {
			flagProfile = name
		} else {
			flagContext, flagNamespace = ctxName, ns
		}
		return nil
	}

	current := currentKubeContext()
	defNS := defaultNamespace
	if pinned != nil && pinned.Context != "" {
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
