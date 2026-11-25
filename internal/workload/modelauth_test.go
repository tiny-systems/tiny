package workload

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/settings"
)

// buildWith builds the session pod in a namespace whose switchboard holds
// data, with the egress proxy Service at 10.96.0.77.
func buildWith(t *testing.T, agent string, data map[string]string) (corev1.PodSpec, error) {
	t.Helper()
	s := testSession("u1")
	s.Spec.Agent = agent
	proxy := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: egressService},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.77"},
	}
	c := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t)).WithObjects(&proxy,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: settings.Name}, Data: data}).Build()
	spec, _, err := buildPodSpec(context.Background(), c, Images{Agent: "a", Sidecar: "s"}, s, true)
	return spec, err
}

// envOf returns the value the container will see for name: the last
// entry wins, and an entry in env beats anything from envFrom.
func envOf(c corev1.Container, name string) (string, bool) {
	val, ok := "", false
	for _, e := range c.Env {
		if e.Name == name {
			val, ok = e.Value, true
		}
	}
	return val, ok
}

// Behind the gateway the agent holds a placeholder of the stored kind and
// talks to the proxy; the real credential's names are blanked so a key
// still in tiny-agent-env cannot reach the container through envFrom.
func TestGatewayGivesTheAgentOnlyAPlaceholder(t *testing.T) {
	spec, err := buildWith(t, "", map[string]string{"egressProxy": "true", settings.KeyModelGateway: settings.GatewayOAuth})
	if err != nil {
		t.Fatal(err)
	}
	agent := spec.Containers[0]
	if v, _ := envOf(agent, envBaseURL); v != "http://10.96.0.77:8080" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", v)
	}
	if v, _ := envOf(agent, envOAuthToken); v != placeholderOAuth {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN = %q, want the placeholder", v)
	}
	for _, name := range []string{envAPIKey, envAuthToken} {
		if v, ok := envOf(agent, name); !ok || v != "" {
			t.Fatalf("%s = %q (set=%v); it must be blanked over envFrom", name, v, ok)
		}
	}
	if v, _ := envOf(agent, "NO_PROXY"); v == "" || v[:len("10.96.0.77")] != "10.96.0.77" {
		t.Fatalf("NO_PROXY %q must start with the proxy's own address", v)
	}
}

func TestGatewayWithAnAPIKeyUsesAnAPIKeyPlaceholder(t *testing.T) {
	spec, err := buildWith(t, "", map[string]string{"egressProxy": "true", settings.KeyModelGateway: settings.GatewayAPIKey})
	if err != nil {
		t.Fatal(err)
	}
	agent := spec.Containers[0]
	if v, _ := envOf(agent, envAPIKey); v != placeholderAPIKey {
		t.Fatalf("ANTHROPIC_API_KEY = %q", v)
	}
	if v, _ := envOf(agent, envOAuthToken); v != "" {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN = %q, want blank", v)
	}
}

// The gateway lives in the proxy; without the proxy there is nowhere to
// send the agent, and starting it with a placeholder would only fail later.
func TestGatewayWithoutTheProxyIsRefused(t *testing.T) {
	if _, err := buildWith(t, "", map[string]string{settings.KeyModelGateway: settings.GatewayOAuth}); err == nil {
		t.Fatal("gateway without the egress proxy must be refused")
	}
}

// Federation mounts an identity token minted for Anthropic and names the
// Console rule; no credential is in the container at all.
func TestFederationMountsAnAnthropicIdentityToken(t *testing.T) {
	spec, err := buildWith(t, "claude", map[string]string{
		settings.KeyFederationRule:           "fdrl_1",
		settings.KeyFederationOrganization:   "org-1",
		settings.KeyFederationServiceAccount: "svac_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := spec.Containers[0]
	var vol *corev1.Volume
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == federationVolume {
			vol = &spec.Volumes[i]
		}
	}
	if vol == nil || vol.Projected == nil {
		t.Fatal("no projected identity token volume")
	}
	tok := vol.Projected.Sources[0].ServiceAccountToken
	if tok.Audience != settings.DefaultFederationAudience || tok.ExpirationSeconds == nil || *tok.ExpirationSeconds != federationTokenTTL {
		t.Fatalf("token projection %+v", tok)
	}
	want := map[string]string{
		"ANTHROPIC_IDENTITY_TOKEN_FILE": federationTokenDir + "/token",
		"ANTHROPIC_FEDERATION_RULE_ID":  "fdrl_1",
		"ANTHROPIC_ORGANIZATION_ID":     "org-1",
		"ANTHROPIC_SERVICE_ACCOUNT_ID":  "svac_1",
		envOAuthToken:                   "",
		envAPIKey:                       "",
	}
	for k, v := range want {
		if got, ok := envOf(agent, k); !ok || got != v {
			t.Errorf("%s = %q (set=%v), want %q", k, got, ok, v)
		}
	}
	if _, ok := envOf(agent, "ANTHROPIC_WORKSPACE_ID"); ok {
		t.Error("no workspace configured, none should be set")
	}
	if _, ok := envOf(agent, envBaseURL); ok {
		t.Error("federation talks to Anthropic directly; no base URL")
	}
}

// Federation outranks the gateway: it stores nothing at all.
func TestFederationWinsOverTheGateway(t *testing.T) {
	spec, err := buildWith(t, "", map[string]string{
		"egressProxy": "true", settings.KeyModelGateway: settings.GatewayOAuth,
		settings.KeyFederationRule: "fdrl_1", settings.KeyFederationOrganization: "org-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := envOf(spec.Containers[0], envBaseURL); ok {
		t.Fatal("with federation on, the gateway must not be wired")
	}
}

// Codex and command sessions are not Claude: neither mode touches them.
func TestModelAuthLeavesOtherAgentsAlone(t *testing.T) {
	for _, agent := range []string{"codex"} {
		spec, err := buildWith(t, agent, map[string]string{
			"egressProxy": "true", settings.KeyModelGateway: settings.GatewayOAuth,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := envOf(spec.Containers[0], envBaseURL); ok {
			t.Fatalf("%s session got the Claude gateway", agent)
		}
	}
}
