package workload

// The pod is the boundary. What keeps "an attacker owns the agent's uid"
// from becoming "an attacker owns the node" is the container's own
// hardening, so every container in the session pod is confined unless
// the session says otherwise — and with the proxy on, the proxy is the
// only nameserver a session can reach.

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/settings"
)

func podFor(t *testing.T, s *agentsv1.Session, objs ...corev1.Service) corev1.PodSpec {
	t.Helper()
	b := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t))
	for i := range objs {
		b = b.WithObjects(&objs[i])
	}
	spec, _, err := buildPodSpec(context.Background(), b.Build(), Images{Agent: "a", Sidecar: "s"}, s)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func allContainers(spec corev1.PodSpec) []corev1.Container {
	return append(slices.Clone(spec.InitContainers), spec.Containers...)
}

func TestEveryContainerIsConfinedByDefault(t *testing.T) {
	spec := podFor(t, testSession("u1"))
	if spec.SecurityContext == nil || spec.SecurityContext.SeccompProfile == nil ||
		spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("pod runs without the runtime's seccomp profile")
	}
	for _, c := range allContainers(spec) {
		sc := c.SecurityContext
		if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			t.Fatalf("%s: privilege escalation allowed — a setuid binary in the image would work", c.Name)
		}
		if sc.Capabilities == nil || !slices.Contains(sc.Capabilities.Drop, "ALL") {
			t.Fatalf("%s: capabilities not dropped", c.Name)
		}
		switch c.Name {
		case "workspace-perms":
			if !slices.Equal(sc.Capabilities.Add, []corev1.Capability{"CHOWN"}) {
				t.Fatalf("workspace-perms keeps %v, want exactly CHOWN — it exists to chown one directory", sc.Capabilities.Add)
			}
		default:
			if len(sc.Capabilities.Add) != 0 {
				t.Fatalf("%s: keeps capabilities %v", c.Name, sc.Capabilities.Add)
			}
		}
	}
}

func TestUnconfinedLiftsTheHardeningAndNothingElse(t *testing.T) {
	s := testSession("u1")
	s.Spec.Unconfined = true
	spec := podFor(t, s)
	if spec.SecurityContext.SeccompProfile != nil {
		t.Fatal("unconfined session still has a seccomp profile — rootless buildah's unshare would fail")
	}
	for _, c := range allContainers(spec) {
		if c.SecurityContext != nil && c.SecurityContext.Capabilities != nil {
			t.Fatalf("%s: capabilities still restricted when unconfined", c.Name)
		}
	}
	// Non-root stays non-root: unconfined is not "root".
	agent := spec.Containers[0]
	if agent.SecurityContext == nil || agent.SecurityContext.RunAsNonRoot == nil || !*agent.SecurityContext.RunAsNonRoot {
		t.Fatal("unconfined dropped RunAsNonRoot")
	}
}

func TestProxyIsTheOnlyResolverWhenOn(t *testing.T) {
	s := testSession("u1")
	proxy := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: egressService},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.77"},
	}
	b := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t)).WithObjects(&proxy,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: settings.Name}, Data: map[string]string{"egressProxy": "true"}})
	spec, _, err := buildPodSpec(context.Background(), b.Build(), Images{Agent: "a", Sidecar: "s"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if spec.DNSPolicy != corev1.DNSNone || spec.DNSConfig == nil {
		t.Fatal("proxy on, but the pod still uses the cluster resolver — a lookup can carry data out through its upstream")
	}
	if !slices.Equal(spec.DNSConfig.Nameservers, []string{"10.96.0.77"}) {
		t.Fatalf("nameservers %v, want the proxy's ClusterIP only", spec.DNSConfig.Nameservers)
	}
	if !slices.Contains(spec.DNSConfig.Searches, "ns.svc.cluster.local") {
		t.Fatalf("searches %v: bare service names like tiny-minio would stop resolving", spec.DNSConfig.Searches)
	}
	if !slices.ContainsFunc(spec.Containers[0].Env, func(e corev1.EnvVar) bool {
		return e.Name == "HTTPS_PROXY" && e.Value == "http://10.96.0.77:3128"
	}) {
		t.Fatal("HTTPS_PROXY should name the same address the resolver does")
	}
}

func TestClusterResolverStaysWhenProxyIsOff(t *testing.T) {
	spec := podFor(t, testSession("u1"))
	if spec.DNSPolicy != "" || spec.DNSConfig != nil {
		t.Fatal("without the proxy the pod must keep ClusterFirst DNS")
	}
}
