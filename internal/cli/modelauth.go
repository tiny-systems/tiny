/*
tiny gateway and tiny federation keep the Claude credential out of agent
pods, the biggest item that stays open on a stock install.

The gateway moves the credential (a subscription token or an API key)
into a Secret only the egress proxy mounts; agents hold a placeholder and
send model calls through the proxy, which attaches the real credential.
Federation stores nothing: each pod's projected service-account token is
exchanged with Anthropic for a short-lived access token, under a rule set
up in the Claude Console. It's for API billing; a subscription can't be
federated.
*/
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/discovery"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/settings"
)

// restartHint is what running sessions need after either switch: their pod
// was built with the old environment.
// Subcommand words for the two switches.
const (
	wordOn  = "on"
	wordOff = "off"
	wordSet = "set"
)

const restartHint = "  sessions already running keep their old environment until their pod is replaced:\n" +
	"    tiny pause --all && tiny resume --all"

func newGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway [on|off]",
		Short: "Keep the Claude credential in the egress proxy instead of agent pods",
		Long: "With the gateway on, the namespace's Claude credential (a `claude setup-token`\n" +
			"or an API key) moves out of tiny-agent-env, which every agent pod reads, into a\n" +
			"Secret only the egress proxy mounts. Agents get a placeholder and send model calls\n" +
			"to the proxy, which attaches the real credential on the way out. A compromised\n" +
			"session has no key to take. Needs the egress allow-list (tiny egress). Codex\n" +
			"sessions are not affected.\n\n" +
			"Bare `tiny gateway` says whether it's on.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{wordOn, wordOff},
		RunE: func(cmd *cobra.Command, args []string) error {
			store, ctx, cancel, err := modelAuthStore(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			switch {
			case len(args) == 0:
				kind, err := store.GatewayState(ctx)
				if err != nil {
					return err
				}
				if kind == "" {
					fmt.Println("  gateway off: agent pods read the Claude credential from tiny-agent-env")
					return nil
				}
				fmt.Printf("  gateway on (%s): the Claude credential is in the egress proxy only\n", gatewayKind(kind))
				return nil
			case args[0] == wordOn:
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				kind, err := store.GatewayOn(ctx)
				if errors.Is(err, sessions.ErrNoClaudeCredential) {
					return fmt.Errorf("%w (it stores the token the gateway will hold)", err)
				}
				if err != nil {
					return err
				}
				fmt.Printf("  ✓ gateway on: the %s is now in the egress proxy only, and new Claude sessions hold a placeholder\n", gatewayKind(kind))
				fmt.Println(restartHint)
				return nil
			case args[0] == wordOff:
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				if err := store.GatewayOff(ctx); err != nil {
					return err
				}
				fmt.Println("  ✓ gateway off: the Claude credential is back in tiny-agent-env for new sessions")
				return nil
			}
			return fmt.Errorf("say on or off")
		},
	}
	return cmd
}

func gatewayKind(kind string) string {
	if kind == settings.GatewayAPIKey {
		return "API key"
	}
	return "subscription token"
}

func newFederationCmd() *cobra.Command {
	var f settings.Federation
	var removeStored bool
	cmd := &cobra.Command{
		Use:   "federation [set|off]",
		Short: "Sign agents in with Anthropic Workload Identity Federation: no stored Claude credential",
		Long: "Federation signs Claude sessions in without any stored credential. Each agent pod\n" +
			"gets a service-account token minted for Anthropic; Claude Code exchanges it for a\n" +
			"short-lived access token under a federation rule you create in the Claude Console.\n" +
			"Usage is billed to the Console organization (API pricing); subscriptions can't be\n" +
			"federated, so use `tiny gateway` for those.\n\n" +
			"Bare `tiny federation` prints what the Console asks for: this cluster's issuer, its\n" +
			"signing keys for inline mode, and the subject and audience to match. Then:\n" +
			"  tiny federation set --rule fdrl_... --organization <org id> --service-account svac_...",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{wordSet, wordOff},
		RunE: func(cmd *cobra.Command, args []string) error {
			store, ctx, cancel, err := modelAuthStore(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			switch {
			case len(args) == 0:
				return showFederation(ctx, store)
			case args[0] == wordSet:
				if f.Audience == "" {
					f.Audience = settings.DefaultFederationAudience
				}
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				if err := store.SetFederation(ctx, f); err != nil {
					return err
				}
				fmt.Println("  ✓ federation on: new Claude sessions exchange their identity token instead of holding a credential")
				return offerCredentialRemoval(ctx, store, removeStored)
			case args[0] == wordOff:
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				if err := store.ClearFederation(ctx); err != nil {
					return err
				}
				fmt.Println("  ✓ federation off: new sessions use the credential in tiny-agent-env (tiny setup stores one)")
				return nil
			}
			return fmt.Errorf("say set or off")
		},
	}
	cmd.Flags().StringVar(&f.RuleID, "rule", "", "federation rule ID (fdrl_...)")
	cmd.Flags().StringVar(&f.OrganizationID, "organization", "", "Claude Console organization ID")
	cmd.Flags().StringVar(&f.ServiceAccountID, "service-account", "", "Console service account ID (svac_...)")
	cmd.Flags().StringVar(&f.WorkspaceID, "workspace", "", "workspace ID (wrkspc_...), when the rule covers several workspaces")
	cmd.Flags().StringVar(&f.Audience, "audience", "", "audience for the identity token; must match the rule (default "+settings.DefaultFederationAudience+")")
	cmd.Flags().BoolVar(&removeStored, "remove-stored-credential", false, "also delete the Claude credential from tiny-agent-env without asking")
	return cmd
}

// offerCredentialRemoval deals with a Claude credential still stored in
// tiny-agent-env: pods blank it, but federation's point is storing none.
func offerCredentialRemoval(ctx context.Context, store *sessions.Store, force bool) error {
	name, err := store.ClaudeCredentialInAgentEnv(ctx)
	if err != nil || name == "" {
		fmt.Println(restartHint)
		return err
	}
	if !force {
		fmt.Printf("  tiny-agent-env still holds %s. Pods no longer see it, but it's stored.\n", name)
		if !stdinIsTTY() {
			fmt.Println("  remove it with: tiny federation set ... --remove-stored-credential")
			fmt.Println(restartHint)
			return nil
		}
		fmt.Print("  Delete it now? Codex credentials stay. [y/N] ")
		if !confirmed(readLine()) {
			fmt.Println(restartHint)
			return nil
		}
	}
	if err := store.RemoveClaudeCredential(ctx); err != nil {
		return err
	}
	fmt.Printf("  ✓ %s deleted from tiny-agent-env\n", name)
	fmt.Println(restartHint)
	return nil
}

// showFederation prints the cluster facts the Claude Console wizard asks
// for, and the current configuration.
func showFederation(ctx context.Context, store *sessions.Store) error {
	f, err := store.FederationState(ctx)
	if err != nil {
		return err
	}
	if f.On() {
		fmt.Printf("  federation on: rule %s, organization %s", f.RuleID, f.OrganizationID)
		if f.ServiceAccountID != "" {
			fmt.Printf(", service account %s", f.ServiceAccountID)
		}
		if f.WorkspaceID != "" {
			fmt.Printf(", workspace %s", f.WorkspaceID)
		}
		fmt.Println()
	} else {
		fmt.Println("  federation off")
	}
	issuer, jwks, err := clusterOIDC(ctx, store.Kube)
	if err != nil {
		return fmt.Errorf("read this cluster's service-account issuer: %w", err)
	}
	audience := f.Audience
	if audience == "" {
		audience = settings.DefaultFederationAudience
	}
	fmt.Println("\n  For the Claude Console (Settings → Workload identity → Connect workload → Kubernetes):")
	fmt.Printf("    issuer URL:  %s\n", issuer)
	fmt.Printf("    subject:     system:serviceaccount:%s:tiny-session\n", store.Kube.Namespace)
	fmt.Printf("    audience:    %s\n", audience)
	fmt.Println("\n  If the issuer isn't reachable from the internet, choose inline keys and paste:")
	fmt.Println(jwks)
	fmt.Println("\n  Then: tiny federation set --rule fdrl_... --organization <org id> --service-account svac_...")
	return nil
}

// clusterOIDC returns the API server's service-account issuer and its
// signing keys as the JSON array the Console's inline mode takes.
func clusterOIDC(ctx context.Context, k *kube.Client) (issuer, keys string, err error) {
	dc, err := discovery.NewDiscoveryClientForConfig(k.RESTConfig)
	if err != nil {
		return "", "", err
	}
	raw, err := dc.RESTClient().Get().AbsPath("/.well-known/openid-configuration").DoRaw(ctx)
	if err != nil {
		return "", "", err
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", err
	}
	raw, err = dc.RESTClient().Get().AbsPath("/openid/v1/jwks").DoRaw(ctx)
	if err != nil {
		return doc.Issuer, "", err
	}
	var set struct {
		Keys json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		return doc.Issuer, "", err
	}
	var pretty any
	_ = json.Unmarshal(set.Keys, &pretty)
	out, _ := json.MarshalIndent(pretty, "    ", "  ")
	return doc.Issuer, "    " + string(out), nil
}

func modelAuthStore(cmd *cobra.Command) (*sessions.Store, context.Context, context.CancelFunc, error) {
	k, err := sessionKube()
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	return newStore(k), ctx, cancel, nil
}
