package addons

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		networkingv1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme,
		discoveryv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func applyPolicy(t *testing.T, objs ...runtime.Object) networkingv1.NetworkPolicy {
	t.Helper()
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithRuntimeObjects(objs...).Build()}
	if err := r.EnsureEgressPolicy(context.Background(), "team-a", false); err != nil {
		t.Fatal(err)
	}
	var pol networkingv1.NetworkPolicy
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: egressPolicyName}, &pol); err != nil {
		t.Fatal(err)
	}
	return pol
}

// The metadata endpoint is how agents have been talked into handing over
// an instance's cloud credentials. It must not be reachable.
func TestEgressPolicyExcludesCloudMetadata(t *testing.T) {
	pol := applyPolicy(t)
	for _, rule := range pol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock == nil || peer.IPBlock.CIDR != cidrAnywhere {
				continue
			}
			if !slices.Contains(peer.IPBlock.Except, cidrLinkLocal) {
				t.Fatalf("link-local not excluded from the internet rule: %v", peer.IPBlock.Except)
			}
			return
		}
	}
	t.Fatal("no internet rule found")
}

// Only http and https leave the namespace: no SSH, no arbitrary ports.
func TestEgressPolicyLimitsInternetPorts(t *testing.T) {
	pol := applyPolicy(t)
	for _, rule := range pol.Spec.Egress {
		if len(rule.To) == 0 || rule.To[0].IPBlock == nil {
			continue
		}
		var ports []int32
		for _, p := range rule.Ports {
			ports = append(ports, p.Port.IntVal)
		}
		slices.Sort(ports)
		if !slices.Equal(ports, []int32{80, 443}) {
			t.Fatalf("internet rule opens %v, want [80 443]", ports)
		}
		return
	}
	t.Fatal("no internet rule found")
}

// Sessions hand each other files and reach each other's exposed ports.
// Containment must not break that.
func TestEgressPolicyKeepsNamespaceTrafficAndDNS(t *testing.T) {
	pol := applyPolicy(t)
	var inNamespace, dns bool
	for _, rule := range pol.Spec.Egress {
		if len(rule.To) == 1 && rule.To[0].PodSelector != nil && len(rule.To[0].PodSelector.MatchLabels) == 0 {
			inNamespace = true
		}
		for _, p := range rule.Ports {
			if p.Port.IntVal == 53 {
				dns = true
			}
		}
	}
	if !inNamespace {
		t.Error("in-namespace egress was not allowed — the artifact store and expose_port would break")
	}
	if !dns {
		t.Error("DNS was not allowed — nothing would resolve")
	}
}

// Re-applying must update, not fail: an upgrade tightens the rules and a
// stale policy is a false sense of containment.
func TestEgressPolicyIsIdempotentAndUpdates(t *testing.T) {
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx := context.Background()
	for range 2 {
		if err := r.EnsureEgressPolicy(ctx, "team-a", false); err != nil {
			t.Fatal(err)
		}
	}
	var pol networkingv1.NetworkPolicy
	if err := r.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: egressPolicyName}, &pol); err != nil {
		t.Fatal(err)
	}
	if len(pol.Spec.Egress) != 3 {
		t.Fatalf("want 3 egress rules after re-apply, got %d", len(pol.Spec.Egress))
	}
}

// A policy no CNI enforces is worse than none, because it reads as
// protection. Never report enforcement that was not found.
func TestPolicyEnforcementReportsHonestly(t *testing.T) {
	ds := func(name string) *appsv1.DaemonSet {
		return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: nsKubeSystem, Name: name}}
	}
	cases := []struct {
		name    string
		objs    []runtime.Object
		wantCNI string
		wantOK  bool
	}{
		{cniCalico, []runtime.Object{ds("calico-node")}, cniCalico, true},
		{cniCilium, []runtime.Object{ds(cniCilium)}, cniCilium, true},
		{"flannel is not an enforcer", []runtime.Object{ds("kube-flannel-ds")}, "", false},
		{"empty cluster", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithRuntimeObjects(tc.objs...).Build()}
			cni, ok := r.PolicyEnforcement(context.Background())
			if cni != tc.wantCNI || ok != tc.wantOK {
				t.Fatalf("got (%q, %v), want (%q, %v)", cni, ok, tc.wantCNI, tc.wantOK)
			}
		})
	}
}

// The state line is what the operator reads. It must say NOT ENFORCED
// when nothing will act on the policy.
func TestEgressStateWarnsWhenNothingEnforces(t *testing.T) {
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx := context.Background()
	if got := r.EgressState(ctx, "team-a"); got != "" {
		t.Fatalf("no policy should report nothing, got %q", got)
	}
	if err := r.EnsureEgressPolicy(ctx, "team-a", false); err != nil {
		t.Fatal(err)
	}
	got := r.EgressState(ctx, "team-a")
	if got != "NOT ENFORCED — no NetworkPolicy-capable CNI found" {
		t.Fatalf("want the NOT ENFORCED warning, got %q", got)
	}
}

// With the proxy on, the internet rule must be GONE. If it survives, the
// allow-list is decoration: a session could route around the proxy by
// talking to an address directly.
func TestProxyModeRemovesTheInternetRule(t *testing.T) {
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx := context.Background()
	if err := r.EnsureEgressPolicy(ctx, "team-a", true); err != nil {
		t.Fatal(err)
	}
	var pol networkingv1.NetworkPolicy
	if err := r.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: egressPolicyName}, &pol); err != nil {
		t.Fatal(err)
	}
	for _, rule := range pol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock != nil && peer.IPBlock.CIDR == cidrAnywhere {
				t.Fatal("internet rule still present with the proxy on — sessions could bypass the allow-list")
			}
		}
	}
}

// DNS narrows to the cluster's own resolvers, closing the tunnel to an
// attacker-run nameserver. Link-local stays open on 53 ONLY, for
// NodeLocal DNSCache — that must not reopen the metadata endpoint.
func TestProxyModeNarrowsDNSWithoutReopeningMetadata(t *testing.T) {
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx := context.Background()
	if err := r.EnsureEgressPolicy(ctx, "team-a", true); err != nil {
		t.Fatal(err)
	}
	var pol networkingv1.NetworkPolicy
	if err := r.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: egressPolicyName}, &pol); err != nil {
		t.Fatal(err)
	}
	var linkLocalPorts []int32
	for _, rule := range pol.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock == nil || peer.IPBlock.CIDR != cidrLinkLocal {
				continue
			}
			if len(rule.Ports) == 0 {
				t.Fatal("link-local allowed on ALL ports — that reopens the metadata endpoint")
			}
			for _, p := range rule.Ports {
				linkLocalPorts = append(linkLocalPorts, p.Port.IntVal)
			}
		}
	}
	if len(linkLocalPorts) == 0 {
		t.Fatal("no link-local DNS rule — NodeLocal DNSCache clusters would stop resolving")
	}
	for _, p := range linkLocalPorts {
		if p != 53 {
			t.Fatalf("link-local open on port %d, want 53 only", p)
		}
	}
}

// k3s enforces NetworkPolicy from a controller inside the k3s process,
// with no DaemonSet to find. Reporting NOT ENFORCED there tells an
// operator they are unprotected when they are not.
func TestPolicyEnforcementDetectsK3s(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "maksym"},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.31.5+k3s1"},
		},
	}
	r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithRuntimeObjects(node).Build()}
	cni, ok := r.PolicyEnforcement(context.Background())
	if !ok {
		t.Fatal("k3s reported as not enforcing — it does, via embedded kube-router")
	}
	if cni != cniK3s {
		t.Fatalf("cni = %q, want %q", cni, cniK3s)
	}
}

// The EKS and AKS CNI DaemonSets exist whether or not policy enforcement
// is enabled, so their presence must NOT be read as enforcement.
// Over-claiming is the worse failure for a security display.
func TestPolicyEnforcementDoesNotTrustAmbiguousCNIs(t *testing.T) {
	for _, name := range []string{"aws-node", "azure-cni"} {
		t.Run(name, func(t *testing.T) {
			ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: nsKubeSystem, Name: name}}
			r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithRuntimeObjects(ds).Build()}
			if cni, ok := r.PolicyEnforcement(context.Background()); ok {
				t.Fatalf("claimed enforcement by %q from a DaemonSet that proves nothing", cni)
			}
		})
	}
}

// The agent reads its own spec.inbox from the Kubernetes API. That API
// lives on a private address — a ClusterIP in 10.0.0.0/8, behind an
// endpoint that is usually a node address in 192.168/16 — both of which
// the internet rule cuts. Without an explicit rule the policy silently
// stops work being delivered while the session looks perfectly healthy.
func TestEgressPolicyAllowsTheAPIServer(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "kubernetes"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.43.0.1",
			Ports:     []corev1.ServicePort{{Port: 443}},
		},
	}
	apiPort := int32(6443)
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "kubernetes",
			Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"192.168.31.76"}}},
		Ports:     []discoveryv1.EndpointPort{{Port: &apiPort}},
	}
	for _, viaProxy := range []bool{false, true} {
		t.Run(map[bool]string{false: "policy only", true: "with proxy"}[viaProxy], func(t *testing.T) {
			r := &Applier{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).
				WithRuntimeObjects(svc, eps).Build()}
			ctx := context.Background()
			if err := r.EnsureEgressPolicy(ctx, "team-a", viaProxy); err != nil {
				t.Fatal(err)
			}
			var pol networkingv1.NetworkPolicy
			if err := r.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: egressPolicyName}, &pol); err != nil {
				t.Fatal(err)
			}
			var gotClusterIP, gotEndpoint bool
			for _, rule := range pol.Spec.Egress {
				for _, peer := range rule.To {
					if peer.IPBlock == nil {
						continue
					}
					switch peer.IPBlock.CIDR {
					case "10.43.0.1/32":
						gotClusterIP = true
					case "192.168.31.76/32":
						gotEndpoint = true
					}
				}
			}
			if !gotClusterIP {
				t.Error("API ClusterIP not allowed — the agent cannot read its inbox")
			}
			if !gotEndpoint {
				t.Error("API endpoint not allowed — most CNIs match egress after DNAT")
			}
		})
	}
}
