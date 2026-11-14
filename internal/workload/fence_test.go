package workload

// A NetworkPolicy reaches a new pod a moment after the pod starts: on k3s
// a session pod's first connection got past the fence. With the fence on,
// the agent waits until the fence blocks a probe before it starts.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/settings"
)

func podWithSettings(t *testing.T, data map[string]string) corev1.PodSpec {
	t.Helper()
	b := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t))
	if data != nil {
		b = b.WithObjects(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: settings.Name},
			Data:       data,
		})
	}
	s := testSession("u1")
	s.Namespace = "ns"
	spec, _, err := buildPodSpec(context.Background(), b.Build(), Images{Agent: "agent:1", Sidecar: "s"}, s, false)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestFencedSessionWaitsForTheFence(t *testing.T) {
	for name, data := range map[string]map[string]string{
		"fence":       {"egress": "true"},
		"fence+proxy": {"egress": "true", "egressProxy": "true"},
	} {
		spec := podWithSettings(t, data)
		last := spec.InitContainers[len(spec.InitContainers)-1]
		if last.Name != fenceContainer {
			t.Fatalf("%s: last init container %q, want %q: the agent must start after the fence is live", name, last.Name, fenceContainer)
		}
		if last.Image != "agent:1" {
			t.Errorf("%s: image %q, want the agent image (already pulled, has bash)", name, last.Image)
		}
		var hostIP bool
		for _, e := range last.Env {
			if e.Name == "HOST_IP" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "status.hostIP" {
				hostIP = true
			}
		}
		if !hostIP {
			t.Errorf("%s: no HOST_IP from status.hostIP: %+v", name, last.Env)
		}
		script := strings.Join(last.Command, " ")
		if !strings.Contains(script, "/dev/tcp/$HOST_IP/10250") {
			t.Errorf("%s: the probe is not the node's kubelet port: %s", name, script)
		}
	}
}

func TestUnfencedSessionStartsAtOnce(t *testing.T) {
	for name, data := range map[string]map[string]string{
		"no settings": nil,
		"fence off":   {"egress": "false"},
	} {
		for _, c := range podWithSettings(t, data).InitContainers {
			if c.Name == fenceContainer {
				t.Errorf("%s: a fence wait with no fence", name)
			}
		}
	}
}
