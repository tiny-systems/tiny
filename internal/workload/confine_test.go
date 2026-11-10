package workload

// The pod is the boundary. What keeps "an attacker owns the agent's uid"
// from becoming "an attacker owns the node" is the container's own
// hardening, so every container in the session pod is confined unless
// the session says otherwise — and with the proxy on, the proxy is the
// only nameserver a session can reach.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/settings"
)

func podFor(t *testing.T, s *agentsv1.Session) corev1.PodSpec {
	t.Helper()
	b := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t))
	spec, _, err := buildPodSpec(context.Background(), b.Build(), Images{Agent: "a", Sidecar: "s"}, s, false)
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
	spec, _, err := buildPodSpec(context.Background(), b.Build(), Images{Agent: "a", Sidecar: "s"}, s, false)
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

func TestPlacementReachesThePod(t *testing.T) {
	s := testSession("u1")
	s.Spec.NodeSelector = map[string]string{"pool": "runs"}
	s.Spec.Tolerations = []corev1.Toleration{{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "runs", Effect: corev1.TaintEffectNoSchedule}}
	spec := podFor(t, s)
	if spec.NodeSelector["pool"] != "runs" {
		t.Fatalf("nodeSelector = %v", spec.NodeSelector)
	}
	if len(spec.Tolerations) != 1 || spec.Tolerations[0].Value != "runs" {
		t.Fatalf("tolerations = %v", spec.Tolerations)
	}
	if spec := podFor(t, testSession("u2")); spec.NodeSelector != nil || spec.Tolerations != nil {
		t.Fatal("a session without placement must schedule anywhere")
	}
}

// A sandboxed runtime is how the shared-kernel path out of the container
// closes. The pod must carry the class exactly when the session asks.
func TestRuntimeClassReachesThePod(t *testing.T) {
	s := testSession("u1")
	s.Spec.RuntimeClassName = "gvisor"
	gv := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"}
	c := clientfake.NewClientBuilder().WithScheme(runtimeScheme(t)).WithObjects(gv).Build()
	if err := checkRuntimeClass(context.Background(), c, s); err != nil {
		t.Fatalf("existing runtime class refused: %v", err)
	}
	spec := podFor(t, s)
	if spec.RuntimeClassName == nil || *spec.RuntimeClassName != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", spec.RuntimeClassName)
	}
	if spec := podFor(t, testSession("u2")); spec.RuntimeClassName != nil {
		t.Fatal("a session without a runtime class must use the cluster default")
	}
}

// Naming a runtime the cluster doesn't have would otherwise create a
// Deployment whose pod is rejected at admission, with the reason buried in
// a ReplicaSet event. Refuse up front and say what to do.
func TestMissingRuntimeClassIsRefusedLoudly(t *testing.T) {
	s := testSession("u1")
	s.Spec.RuntimeClassName = "gvisor"
	c := clientfake.NewClientBuilder().WithScheme(runtimeScheme(t)).Build()
	err := checkRuntimeClass(context.Background(), c, s)
	if err == nil || !strings.Contains(err.Error(), `"gvisor" does not exist`) {
		t.Fatalf("want a clear refusal, got %v", err)
	}
	if err := checkRuntimeClass(context.Background(), c, testSession("u2")); err != nil {
		t.Fatalf("no runtime class asked for, but got %v", err)
	}
}

// RuntimeClasses are cluster-scoped and namespace-only users usually can't
// read them. That must not block session creation.
func TestUnreadableRuntimeClassGetsTheBenefitOfTheDoubt(t *testing.T) {
	s := testSession("u1")
	s.Spec.RuntimeClassName = "gvisor"
	c := interceptor.NewClient(clientfake.NewClientBuilder().WithScheme(runtimeScheme(t)).Build(), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*nodev1.RuntimeClass); ok {
				return apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, key.Name, errors.New("namespace-scoped user"))
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	if err := checkRuntimeClass(context.Background(), c, s); err != nil {
		t.Fatalf("forbidden lookup should not block creation, got %v", err)
	}
}

func runtimeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := workspaceScheme(t)
	if err := nodev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}
