package sessions

import (
	"errors"
	"maps"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
)

func modelAuthStore(t *testing.T, objs ...client.Object) (*Store, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, agentsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Store{Kube: &kube.Client{Client: c, Namespace: "agents"}}, c
}

func switchboard(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: settingsCM}, Data: data}
}

func agentEnv(data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: workload.AgentEnvSecret}, Data: d}
}

func secretKeys(t *testing.T, c client.Client, name string) map[string]string {
	t.Helper()
	sec := &corev1.Secret{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: name}, sec); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range sec.Data {
		out[k] = string(v)
	}
	maps.Copy(out, sec.StringData)
	return out
}

// Turning the gateway on moves the subscription token out of the Secret
// every agent pod reads, into the one only the proxy mounts, and leaves
// the codex credential where it was.
func TestGatewayOnMovesTheClaudeCredential(t *testing.T) {
	store, c := modelAuthStore(t,
		switchboard(map[string]string{keyEgress: trueWord, keyEgressProxy: trueWord}),
		agentEnv(map[string]string{credOAuth: "sk-ant-oat01-real", "OPENAI_API_KEY": "sk-openai"}))
	kind, err := store.GatewayOn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if kind != settings.GatewayOAuth {
		t.Fatalf("kind = %q", kind)
	}
	if got := secretKeys(t, c, workload.ModelCredentialSecret); got[credOAuth] != "sk-ant-oat01-real" {
		t.Fatalf("model credential = %v", got)
	}
	env := secretKeys(t, c, workload.AgentEnvSecret)
	if _, still := env[credOAuth]; still {
		t.Fatal("the token is still in tiny-agent-env, which every agent pod reads")
	}
	if env["OPENAI_API_KEY"] != "sk-openai" {
		t.Fatal("codex's credential must stay in tiny-agent-env")
	}
	if state, _ := store.GatewayState(t.Context()); state != settings.GatewayOAuth {
		t.Fatalf("gateway state = %q", state)
	}
	// The proxy Deployment now mounts the credential.
	dep := &appsv1.Deployment{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: "tiny-egress"}, dep); err != nil {
		t.Fatalf("proxy deployment: %v", err)
	}
	mounted := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == workload.ModelCredentialSecret {
			mounted = true
		}
	}
	if !mounted {
		t.Fatal("the proxy does not mount the model credential")
	}

	// Running it again finds the credential already moved and is fine.
	if _, err := store.GatewayOn(t.Context()); err != nil {
		t.Fatalf("second gateway on: %v", err)
	}
}

func TestGatewayOnWithAnAPIKey(t *testing.T) {
	store, _ := modelAuthStore(t,
		switchboard(map[string]string{keyEgress: trueWord, keyEgressProxy: trueWord}),
		agentEnv(map[string]string{credAPIKey: "sk-ant-api03-real"}))
	kind, err := store.GatewayOn(t.Context())
	if err != nil || kind != settings.GatewayAPIKey {
		t.Fatalf("kind %q err %v", kind, err)
	}
}

func TestGatewayOnRefusals(t *testing.T) {
	cases := map[string]struct {
		data map[string]string
		env  map[string]string
		want string
	}{
		"proxy off":     {map[string]string{keyEgress: trueWord}, map[string]string{credOAuth: "x"}, "egress proxy"},
		"federation on": {map[string]string{keyEgressProxy: trueWord, settings.KeyFederationRule: "fdrl_1", settings.KeyFederationOrganization: "o"}, map[string]string{credOAuth: "x"}, "federation is on"},
		"no credential": {map[string]string{keyEgressProxy: trueWord}, map[string]string{"OPENAI_API_KEY": "x"}, "tiny setup"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store, c := modelAuthStore(t, switchboard(tc.data), agentEnv(tc.env))
			_, err := store.GatewayOn(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
			if name == "no credential" && !errors.Is(err, ErrNoClaudeCredential) {
				t.Fatalf("want ErrNoClaudeCredential, got %v", err)
			}
			if secretKeys(t, c, workload.ModelCredentialSecret) != nil {
				t.Fatal("a refused gateway on created the credential secret")
			}
		})
	}
}

// Off reverses it: the credential goes back, the secret and setting go.
func TestGatewayOffPutsTheCredentialBack(t *testing.T) {
	store, c := modelAuthStore(t,
		switchboard(map[string]string{keyEgress: trueWord, keyEgressProxy: trueWord}),
		agentEnv(map[string]string{credOAuth: "sk-ant-oat01-real"}))
	if _, err := store.GatewayOn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.GatewayOff(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := secretKeys(t, c, workload.AgentEnvSecret); got[credOAuth] != "sk-ant-oat01-real" {
		t.Fatalf("tiny-agent-env = %v", got)
	}
	if secretKeys(t, c, workload.ModelCredentialSecret) != nil {
		t.Fatal("the model credential secret should be gone")
	}
	if state, _ := store.GatewayState(t.Context()); state != "" {
		t.Fatalf("gateway still %q", state)
	}
}

func TestFederationSetAndClear(t *testing.T) {
	store, _ := modelAuthStore(t, switchboard(map[string]string{keyEgress: trueWord}))
	f := settings.Federation{RuleID: "fdrl_1", OrganizationID: "org", ServiceAccountID: "svac_1", WorkspaceID: "wrkspc_1"}
	if err := store.SetFederation(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	got, err := store.FederationState(t.Context())
	if err != nil || got.RuleID != "fdrl_1" || got.WorkspaceID != "wrkspc_1" || got.Audience != settings.DefaultFederationAudience {
		t.Fatalf("state %+v err %v", got, err)
	}
	if err := store.ClearFederation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.FederationState(t.Context()); got.On() {
		t.Fatalf("still on: %+v", got)
	}
}

func TestFederationRefusals(t *testing.T) {
	store, _ := modelAuthStore(t, switchboard(map[string]string{keyEgressProxy: trueWord, settings.KeyModelGateway: settings.GatewayOAuth}))
	for name, f := range map[string]settings.Federation{
		"missing org":   {RuleID: "fdrl_1"},
		"bad rule":      {RuleID: "rule1", OrganizationID: "o"},
		"bad svac":      {RuleID: "fdrl_1", OrganizationID: "o", ServiceAccountID: "acct"},
		"gateway is on": {RuleID: "fdrl_1", OrganizationID: "o"},
	} {
		if err := store.SetFederation(t.Context(), f); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
}
