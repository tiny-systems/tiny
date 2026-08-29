package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tiny-systems/tiny/internal/kube"
)

// newSetupCmd is the wizard that replaces a page of kubectl incantations:
// pin the cluster, install the runtime, store the agent's token, mint a
// repo deploy key. Idempotent — it only offers what is missing.
func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup: cluster, runtime, agent token, repo key",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sessionKube() // pins the target on first contact (asks)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 120*time.Second)
			defer cancel()

			if err := ensureRuntime(ctx, k); err != nil {
				return err
			}
			fmt.Println("  ✓ runtime present")

			if err := setupAgentToken(ctx, k); err != nil {
				return err
			}
			if err := setupCodexToken(ctx, k); err != nil {
				return err
			}
			return setupRepoKey(ctx, k)
		},
	}
}

// setupAgentToken makes sure the tiny-agent-env secret holds a credential
// the agent can start with.
func setupAgentToken(ctx context.Context, k *kube.Client) error {
	existing := &corev1.Secret{}
	getErr := k.Client.Get(ctx, kube.Key(k.Namespace, "tiny-agent-env"), existing)
	secretExists := getErr == nil
	if secretExists && (len(existing.Data["CLAUDE_CODE_OAUTH_TOKEN"]) > 0 || len(existing.Data["ANTHROPIC_API_KEY"]) > 0) {
		// Present is not the same as working — an expired token satisfies
		// this check, so offer the swap.
		fmt.Print("  ✓ agent token present (tiny-agent-env) — replace it? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !confirmed(answer) {
			return nil
		}
	}
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return getErr
	}

	fmt.Println("\n  The agent needs a credential. Get one with `claude setup-token`")
	fmt.Println("  (Claude subscription) or use an Anthropic API key.")
	fmt.Print("  Paste token (input hidden, empty to skip): ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		fmt.Println("  – skipped; sessions will start but claude cannot authenticate")
		return nil
	}
	key := "CLAUDE_CODE_OAUTH_TOKEN"
	if strings.HasPrefix(token, "sk-ant-a") { // api keys, not oauth tokens
		key = "ANTHROPIC_API_KEY"
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tiny-agent-env", Namespace: k.Namespace},
		StringData: map[string]string{key: token},
	}
	if secretExists { // the ReadPassword err above must not shadow this fact
		existing.StringData = map[string]string{key: token}
		if err := k.Client.Update(ctx, existing); err != nil {
			return fmt.Errorf("update tiny-agent-env: %w", err)
		}
	} else if err := k.Client.Create(ctx, secret); err != nil {
		return fmt.Errorf("create tiny-agent-env: %w", err)
	}
	fmt.Printf("  ✓ agent token stored (%s)\n", key)
	return nil
}

// setupCodexToken offers a codex credential for `tiny new --agent codex`:
// the ChatGPT login already on this machine (~/.codex/auth.json) when there
// is one, an OpenAI API key otherwise. Stored beside the claude keys in the
// same tiny-agent-env secret; skipping is fine — codex sessions just won't
// authenticate until it's set.
func setupCodexToken(ctx context.Context, k *kube.Client) error {
	existing := &corev1.Secret{}
	getErr := k.Client.Get(ctx, kube.Key(k.Namespace, "tiny-agent-env"), existing)
	secretExists := getErr == nil
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return getErr
	}
	if secretExists && (len(existing.Data["TINY_CODEX_AUTH_JSON"]) > 0 || len(existing.Data["OPENAI_API_KEY"]) > 0) {
		fmt.Print("  ✓ codex credential present (tiny-agent-env) — replace it? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !confirmed(answer) {
			return nil
		}
	}

	data := map[string]string{}
	home, _ := os.UserHomeDir()
	authPath := filepath.Join(home, ".codex", "auth.json")
	if raw, err := os.ReadFile(authPath); err == nil && len(raw) > 0 {
		fmt.Print("  Found a ChatGPT login for codex (~/.codex/auth.json) — store it for codex sessions? [Y/n] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer == "" || confirmed(answer) {
			data["TINY_CODEX_AUTH_JSON"] = string(raw)
		}
	}
	if len(data) == 0 {
		fmt.Println("\n  Codex sessions (`tiny new --agent codex`) need a credential: a ChatGPT")
		fmt.Println("  login (`codex login` on this machine, then re-run setup) or an OpenAI API key.")
		fmt.Print("  Paste OpenAI API key (input hidden, empty to skip): ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		key := strings.TrimSpace(string(raw))
		if key == "" {
			fmt.Println("  – skipped; codex sessions won't authenticate until a credential is set")
			return nil
		}
		data["OPENAI_API_KEY"] = key
	}

	if secretExists {
		existing.StringData = data
		if err := k.Client.Update(ctx, existing); err != nil {
			return fmt.Errorf("update tiny-agent-env: %w", err)
		}
	} else if err := k.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tiny-agent-env", Namespace: k.Namespace},
		StringData: data,
	}); err != nil {
		return fmt.Errorf("create tiny-agent-env: %w", err)
	}
	for key := range data {
		fmt.Printf("  ✓ codex credential stored (%s)\n", key)
	}
	return nil
}

// setupRepoKey mints a dedicated ed25519 deploy key, stores the private half
// where sessions can use it, and hands the public half to the human — the
// one step only they can do. Personal ~/.ssh keys are never read.
func setupRepoKey(ctx context.Context, k *kube.Client) error {
	existing := &corev1.Secret{}
	getErr := k.Client.Get(ctx, kube.Key(k.Namespace, "tiny-repo-keys"), existing)
	keyExists := getErr == nil
	if keyExists {
		fmt.Println("  ✓ repo key present (tiny-repo-keys)")
		if pub, ok := existing.Data["id_ed25519.pub"]; ok {
			fmt.Printf("    public key: %s", string(pub))
		}
		fmt.Print("    Rotate it? The old key stops working everywhere it was added. [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !confirmed(answer) {
			return nil
		}
	} else {
		if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		fmt.Print("\n  Sessions can clone private repos over SSH with a deploy key\n  minted just for this cluster. Create one? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !confirmed(answer) {
			fmt.Println("  – skipped; public repos and HTTPS tokens still work")
			return nil
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privPEM, err := ssh.MarshalPrivateKey(priv, "tiny-sessions")
	if err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " tiny-sessions\n"

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tiny-repo-keys", Namespace: k.Namespace},
		StringData: map[string]string{
			"id_ed25519":     string(pem.EncodeToMemory(privPEM)),
			"id_ed25519.pub": pubLine,
			"known_hosts":    githubKnownHosts(),
		},
	}
	if keyExists {
		existing.StringData = secret.StringData
		if err := k.Client.Update(ctx, existing); err != nil {
			return fmt.Errorf("rotate tiny-repo-keys: %w", err)
		}
	} else if err := k.Client.Create(ctx, secret); err != nil {
		return fmt.Errorf("create tiny-repo-keys: %w", err)
	}
	fmt.Println("  ✓ deploy key minted and stored (tiny-repo-keys)")
	fmt.Println("\n  Give the PUBLIC half access to your repos:")
	fmt.Println("    per repo:   https://github.com/<owner>/<repo>/settings/keys")
	fmt.Println("    everything: https://github.com/settings/ssh/new")
	fmt.Printf("\n  %s\n", strings.TrimSpace(pubLine))
	return nil
}

// githubKnownHosts pins github.com's published host keys — baked in so setup
// works offline and nothing trusts first use.
// Source: https://docs.github.com/en/authentication/keychain (2024 keys).
func githubKnownHosts() string {
	return `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`
}
