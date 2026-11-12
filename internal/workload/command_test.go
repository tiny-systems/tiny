package workload

// A command session runs one given command with no model: a scripted
// browser replay, say. It keeps every boundary an agent session has —
// hardening, proxy, placement, limits — and gets none of the credentials.

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func agentOf(t *testing.T, spec corev1.PodSpec) corev1.Container {
	t.Helper()
	for _, c := range spec.Containers {
		if c.Name == agentContainer {
			return c
		}
	}
	t.Fatal("no agent container")
	return corev1.Container{}
}

func sessionPair() (claude, command *agentsv1.Session) {
	base := func() *agentsv1.Session {
		s := testSession("u1")
		s.Spec.NodeSelector = map[string]string{"pool": "runs"}
		s.Spec.Tolerations = []corev1.Toleration{{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "runs", Effect: corev1.TaintEffectNoSchedule}}
		s.Spec.Resources = &agentsv1.SessionResources{CPU: "1", Memory: "2Gi"}
		return s
	}
	claude, command = base(), base()
	command.Spec.Agent = commandAgent
	command.Spec.Command = []string{"bp-replay", "--journey-url=http://d/j/t"}
	return claude, command
}

func TestCommandSessionRunsTheCommand(t *testing.T) {
	claudeS, commandS := sessionPair()
	cl, cm := podFor(t, claudeS), podFor(t, commandS)
	a, b := agentOf(t, cl), agentOf(t, cm)
	if !slices.Equal(b.Command, []string{"/tiny/entrypoint.sh"}) {
		t.Fatalf("command = %v", b.Command)
	}
	if !slices.Equal(b.Args, commandS.Spec.Command) {
		t.Fatalf("args = %v, want the session's command", b.Args)
	}
	if len(a.Args) != 0 {
		t.Fatalf("an agent session got args %v", a.Args)
	}
	if v, _ := envValue(b.Env, "TINY_AGENT"); v != "command" {
		t.Fatalf("TINY_AGENT = %q", v)
	}
	if !reflect.DeepEqual(a.SecurityContext, b.SecurityContext) || !reflect.DeepEqual(a.Resources, b.Resources) {
		t.Fatal("a command session must keep the agent container's hardening and resources")
	}
	for _, e := range a.Env {
		if !strings.Contains(strings.ToUpper(e.Name), "PROXY") {
			continue
		}
		if v, ok := envValue(b.Env, e.Name); !ok || v != e.Value {
			t.Fatalf("%s differs: %q vs %q", e.Name, e.Value, v)
		}
	}
	if !reflect.DeepEqual(cl.NodeSelector, cm.NodeSelector) || !reflect.DeepEqual(cl.Tolerations, cm.Tolerations) ||
		!reflect.DeepEqual(cl.RuntimeClassName, cm.RuntimeClassName) || !reflect.DeepEqual(cl.SecurityContext, cm.SecurityContext) {
		t.Fatal("a command session must keep the pod's placement and hardening")
	}
}

func TestCommandSessionHasNoAgentSecret(t *testing.T) {
	claudeS, commandS := sessionPair()
	names := func(c corev1.Container) []string {
		var out []string
		for _, e := range c.EnvFrom {
			if e.SecretRef != nil {
				out = append(out, e.SecretRef.Name)
			}
		}
		return out
	}
	if !slices.Contains(names(agentOf(t, podFor(t, claudeS))), AgentEnvSecret) {
		t.Fatal("an agent session lost its credentials")
	}
	if got := names(agentOf(t, podFor(t, commandS))); len(got) != 0 {
		t.Fatalf("a command session sees secrets %v", got)
	}
}

// Journey code in a command session is untrusted and runs unattended: no
// deploy key, no service account token, and no tiny-mcp sidecar (it serves
// an agent, and a command session has none).
func TestCommandSessionHasNoKeysOrToken(t *testing.T) {
	claudeS, commandS := sessionPair()
	cl, cm := podFor(t, claudeS), podFor(t, commandS)
	mounts := func(c corev1.Container) []string {
		out := make([]string, 0, len(c.VolumeMounts))
		for _, m := range c.VolumeMounts {
			out = append(out, m.Name)
		}
		return out
	}
	if !slices.Contains(mounts(agentOf(t, cl)), repoKeysVolume) {
		t.Fatal("an agent session lost its deploy key")
	}
	if slices.Contains(mounts(agentOf(t, cm)), repoKeysVolume) {
		t.Fatal("a command session mounts the deploy key")
	}
	if cm.AutomountServiceAccountToken == nil || *cm.AutomountServiceAccountToken {
		t.Fatal("a command session gets a service account token")
	}
	if len(cm.Containers) != 1 {
		t.Fatalf("a command session runs %d containers", len(cm.Containers))
	}
	for _, v := range cm.Volumes {
		if v.Secret != nil {
			t.Fatalf("a command session has secret volume %s", v.Name)
		}
	}
}
