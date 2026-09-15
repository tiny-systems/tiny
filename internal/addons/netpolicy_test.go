package addons

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	if err := r.EnsureEgressPolicy(context.Background(), "team-a"); err != nil {
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
			if peer.IPBlock == nil || peer.IPBlock.CIDR != "0.0.0.0/0" {
				continue
			}
			if !slices.Contains(peer.IPBlock.Except, "169.254.0.0/16") {
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
		if err := r.EnsureEgressPolicy(ctx, "team-a"); err != nil {
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
		return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name}}
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
	if err := r.EnsureEgressPolicy(ctx, "team-a"); err != nil {
		t.Fatal(err)
	}
	got := r.EgressState(ctx, "team-a")
	if got != "NOT ENFORCED — no NetworkPolicy-capable CNI found" {
		t.Fatalf("want the NOT ENFORCED warning, got %q", got)
	}
}
