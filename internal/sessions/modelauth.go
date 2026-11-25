package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
)

// The Claude credential names, in Claude Code's own order of preference.
const (
	credOAuth  = "CLAUDE_CODE_OAUTH_TOKEN"
	credAPIKey = "ANTHROPIC_API_KEY"
)

// ErrNoClaudeCredential means there is nothing to move into the gateway.
var ErrNoClaudeCredential = errors.New("no Claude credential stored in this namespace: run tiny setup first")

// GatewayOn moves the namespace's Claude credential out of tiny-agent-env,
// which every agent pod reads, into the model credential Secret, which only
// the egress proxy mounts, and switches sessions to the gateway. It
// returns the credential's kind. Sessions already running keep the old
// credential in their environment until their pod is replaced.
func (s *Store) GatewayOn(ctx context.Context) (string, error) {
	cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
	if err != nil {
		return "", err
	}
	if !cfg.EgressProxy {
		return "", fmt.Errorf("the model gateway runs in the egress proxy, which is off here: turn on the allow-list first (tiny egress)")
	}
	if cfg.Federation.On() {
		return "", fmt.Errorf("federation is on, and it stores no credential at all: turn it off first (tiny federation off) if you want the gateway")
	}
	agentEnv, err := s.secretData(ctx, workload.AgentEnvSecret)
	if err != nil {
		return "", err
	}
	stored, err := s.secretData(ctx, workload.ModelCredentialSecret)
	if err != nil {
		return "", err
	}
	name, value := pickClaudeCredential(agentEnv)
	if name == "" {
		// Already moved (a second `gateway on`), or never set.
		if name, value = pickClaudeCredential(stored); name == "" {
			return "", ErrNoClaudeCredential
		}
	}
	if err := s.writeSecret(ctx, workload.ModelCredentialSecret, map[string]string{name: value}, true); err != nil {
		return "", err
	}
	if err := s.removeSecretKeys(ctx, workload.AgentEnvSecret, credOAuth, credAPIKey); err != nil {
		return "", err
	}
	kind := settings.GatewayOAuth
	if name == credAPIKey {
		kind = settings.GatewayAPIKey
	}
	if err := s.setSettingKeys(ctx, map[string]string{settings.KeyModelGateway: kind}); err != nil {
		return "", err
	}
	// The proxy Deployment gains the credential mount and the gateway port.
	return kind, s.ReapplyAddons(ctx)
}

// GatewayOff puts the credential back into tiny-agent-env and removes the
// model credential Secret. New sessions hold the credential again.
func (s *Store) GatewayOff(ctx context.Context) error {
	stored, err := s.secretData(ctx, workload.ModelCredentialSecret)
	if err != nil {
		return err
	}
	if name, value := pickClaudeCredential(stored); name != "" {
		if err := s.writeSecret(ctx, workload.AgentEnvSecret, map[string]string{name: value}, false); err != nil {
			return err
		}
	}
	if err := s.setSettingKeys(ctx, map[string]string{settings.KeyModelGateway: ""}); err != nil {
		return err
	}
	return client.IgnoreNotFound(s.Kube.Client.Delete(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace, Name: workload.ModelCredentialSecret},
	}))
}

// GatewayState reports the gateway's kind, or "" when it is off.
func (s *Store) GatewayState(ctx context.Context) (string, error) {
	cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
	return cfg.ModelGateway, err
}

// StoreGatewayCredential replaces the credential the gateway attaches —
// what `tiny setup` does when the gateway is on, so a rotated token never
// passes through tiny-agent-env.
func (s *Store) StoreGatewayCredential(ctx context.Context, name, value string) error {
	if name != credOAuth && name != credAPIKey {
		return fmt.Errorf("unknown credential %s", name)
	}
	if err := s.writeSecret(ctx, workload.ModelCredentialSecret, map[string]string{name: value}, true); err != nil {
		return err
	}
	kind := settings.GatewayOAuth
	if name == credAPIKey {
		kind = settings.GatewayAPIKey
	}
	// Agents fake the kind they are told; a new kind needs new pods, but
	// the setting must follow the credential either way.
	return s.setSettingKeys(ctx, map[string]string{settings.KeyModelGateway: kind})
}

// SetFederation stores the Claude Console IDs a session exchanges its
// identity token with. No secret is involved.
func (s *Store) SetFederation(ctx context.Context, f settings.Federation) error {
	if !f.On() {
		return fmt.Errorf("a federation rule ID and an organization ID are both required")
	}
	if !strings.HasPrefix(f.RuleID, "fdrl_") {
		return fmt.Errorf("rule ID %q should start with fdrl_ (shown when the rule is created in the Claude Console)", f.RuleID)
	}
	if f.ServiceAccountID != "" && !strings.HasPrefix(f.ServiceAccountID, "svac_") {
		return fmt.Errorf("service account ID %q should start with svac_", f.ServiceAccountID)
	}
	if f.WorkspaceID != "" && !strings.HasPrefix(f.WorkspaceID, "wrkspc_") {
		return fmt.Errorf("workspace ID %q should start with wrkspc_", f.WorkspaceID)
	}
	cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
	if err != nil {
		return err
	}
	if cfg.ModelGateway != "" {
		return fmt.Errorf("the model gateway is on: turn it off first (tiny gateway off), then remove the Claude credential with tiny setup if federation replaces it")
	}
	return s.setSettingKeys(ctx, map[string]string{
		settings.KeyFederationRule:           f.RuleID,
		settings.KeyFederationOrganization:   f.OrganizationID,
		settings.KeyFederationServiceAccount: f.ServiceAccountID,
		settings.KeyFederationWorkspace:      f.WorkspaceID,
		settings.KeyFederationAudience:       f.Audience,
	})
}

// ClearFederation switches federation off; new sessions use whatever
// credential tiny-agent-env holds.
func (s *Store) ClearFederation(ctx context.Context) error {
	return s.setSettingKeys(ctx, map[string]string{
		settings.KeyFederationRule: "", settings.KeyFederationOrganization: "",
		settings.KeyFederationServiceAccount: "", settings.KeyFederationWorkspace: "",
		settings.KeyFederationAudience: "",
	})
}

// FederationState returns the stored federation configuration.
func (s *Store) FederationState(ctx context.Context) (settings.Federation, error) {
	cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
	return cfg.Federation, err
}

// ClaudeCredentialInAgentEnv reports which Claude credential tiny-agent-env
// still holds, if any. With federation on it outranks federation in Claude
// Code's order, so the pod spec blanks it — but it is still stored.
func (s *Store) ClaudeCredentialInAgentEnv(ctx context.Context) (string, error) {
	data, err := s.secretData(ctx, workload.AgentEnvSecret)
	name, _ := pickClaudeCredential(data)
	return name, err
}

// RemoveClaudeCredential deletes the Claude credential from tiny-agent-env,
// for a namespace that has moved to federation.
func (s *Store) RemoveClaudeCredential(ctx context.Context) error {
	return s.removeSecretKeys(ctx, workload.AgentEnvSecret, credOAuth, credAPIKey)
}

func pickClaudeCredential(data map[string][]byte) (name, value string) {
	for _, k := range []string{credOAuth, credAPIKey} {
		if v := strings.TrimSpace(string(data[k])); v != "" {
			return k, v
		}
	}
	return "", ""
}

func (s *Store) secretData(ctx context.Context, name string) (map[string][]byte, error) {
	sec := &corev1.Secret{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, sec)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secret %s: %w", name, err)
	}
	return sec.Data, nil
}

// writeSecret sets keys in a Secret, creating it when missing. With
// replace, the Secret ends up holding exactly these keys.
func (s *Store) writeSecret(ctx context.Context, name string, data map[string]string, replace bool) error {
	sec := &corev1.Secret{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, sec)
	if apierrors.IsNotFound(err) {
		bytes := map[string][]byte{}
		for k, v := range data {
			bytes[k] = []byte(v)
		}
		return s.Kube.Client.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace, Name: name},
			Data:       bytes,
		})
	}
	if err != nil {
		return err
	}
	if replace {
		sec.Data = map[string][]byte{}
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for k, v := range data {
		sec.Data[k] = []byte(v)
	}
	return s.Kube.Client.Update(ctx, sec)
}

func (s *Store) removeSecretKeys(ctx context.Context, name string, keys ...string) error {
	sec := &corev1.Secret{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, sec)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	changed := false
	for _, k := range keys {
		if _, ok := sec.Data[k]; ok {
			delete(sec.Data, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.Kube.Client.Update(ctx, sec)
}

// setSettingKeys merges keys into the switchboard, creating it with the
// namespace defaults first when it doesn't exist. An empty value deletes
// the key.
func (s *Store) setSettingKeys(ctx context.Context, kv map[string]string) error {
	if err := s.EnsureNamespaceDefaults(ctx); err != nil {
		return err
	}
	cm := &corev1.ConfigMap{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: settingsCM}, cm); err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	for k, v := range kv {
		if v == "" {
			delete(cm.Data, k)
		} else {
			cm.Data[k] = v
		}
	}
	return s.Kube.Client.Update(ctx, cm)
}
