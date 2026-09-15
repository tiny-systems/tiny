/*
The egress policy is the deterministic half of agent containment.

An agent runs with bypassPermissions: inside its pod it can execute
anything. That is a deliberate trade — the value of a real CLI is that it
is not second-guessed — so containment has to come from the network, not
from asking the agent nicely.

What this closes, and what it does not, matters enough to say plainly.
Closed: the cloud metadata endpoint (the documented path to stealing an
instance's cloud credentials), every port except 80/443, and reaching
pods outside this namespace. Open: HTTPS to the internet, because the
agent must reach its model API, and a NetworkPolicy selects addresses,
not hostnames. Data can still leave over 443. Narrowing that needs an
egress proxy the policy points at, which is a later step.

In-namespace traffic stays allowed on purpose: sessions hand each other
files through the artifact store and reach each other's exposed ports.
*/
package addons

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const egressPolicyName = "tiny-session-egress"

// privateRanges are excluded from the internet rule: an agent has no
// business dialing another namespace's pods, another tenant's services,
// or the node itself. 169.254.0.0/16 covers the cloud metadata endpoint
// at 169.254.169.254, which is how agents have been made to hand over
// cloud credentials.
var privateRanges = []string{
	"169.254.0.0/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

func tcp(port int32) networkingv1.NetworkPolicyPort {
	p := intstr.FromInt32(port)
	proto := corev1.ProtocolTCP
	return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &p}
}

// EnsureEgressPolicy applies the default-deny egress policy to every
// session pod in the namespace.
func (r *Applier) EnsureEgressPolicy(ctx context.Context, ns string) error {
	dnsUDP := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)

	internet := []networkingv1.NetworkPolicyPeer{{
		IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: privateRanges},
	}}

	pol := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: egressPolicyName},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "tiny-session"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				// DNS anywhere: kube-dns lives in another namespace, which
				// the internet rule excludes, so it needs its own opening.
				{Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: &dnsUDP, Port: &dnsPort},
					tcp(53),
				}},
				// This namespace: the artifact store and each other's
				// exposed ports. Sessions collaborating is a feature.
				{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
				// The internet, http and https only, private space cut out.
				{To: internet, Ports: []networkingv1.NetworkPolicyPort{tcp(80), tcp(443)}},
			},
		},
	}
	// Update in place when it already exists: an upgrade may tighten the
	// rules, and a stale policy is a false sense of containment.
	var existing networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: egressPolicyName}, &existing)
	if apierrors.IsNotFound(err) {
		if cerr := r.Create(ctx, pol); cerr != nil {
			return fmt.Errorf("create egress policy: %w", cerr)
		}
		return nil
	}
	if err != nil {
		return err
	}
	existing.Spec = pol.Spec
	if uerr := r.Update(ctx, &existing); uerr != nil {
		return fmt.Errorf("update egress policy: %w", uerr)
	}
	return nil
}

// TeardownEgressPolicy removes it. Sessions go back to unrestricted egress.
func (r *Applier) TeardownEgressPolicy(ctx context.Context, ns string) error {
	return r.deleteIfExists(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: egressPolicyName},
	})
}

// knownEnforcers are CNI DaemonSets that implement NetworkPolicy. The
// name is the prefix their DaemonSet carries in kube-system.
const (
	cniCalico = "calico"
	cniCilium = "cilium"
)

var knownEnforcers = map[string]string{
	"calico-node":  cniCalico,
	"cilium":       cniCilium,
	"antrea-agent": "antrea",
	"kube-router":  "kube-router",
	"weave-net":    "weave",
	"ovnkube-node": "ovn-kubernetes",
	"aws-node":     "aws-vpc-cni",
	"azure-cni":    "azure",
}

// PolicyEnforcement reports whether anything in this cluster will act on
// a NetworkPolicy. The API accepts the object regardless — on a cluster
// running flannel, or kind's default CNI, it is silently inert. A
// security control that quietly does nothing is worse than none, so this
// is surfaced rather than assumed.
//
// Returns the CNI's name and true when one is recognised. An empty name
// with false means nothing known was found; an unreadable kube-system
// (no RBAC for it) is reported as unknown too, never as enforced.
func (r *Applier) PolicyEnforcement(ctx context.Context) (string, bool) {
	var dss appsv1.DaemonSetList
	if err := r.List(ctx, &dss, client.InNamespace("kube-system")); err != nil {
		return "", false
	}
	for _, ds := range dss.Items {
		for prefix, name := range knownEnforcers {
			if len(ds.Name) >= len(prefix) && ds.Name[:len(prefix)] == prefix {
				return name, true
			}
		}
	}
	return "", false
}

// EgressState is the line the settings screen shows: what the policy is
// doing, or the warning that it is doing nothing at all.
func (r *Applier) EgressState(ctx context.Context, ns string) string {
	var pol networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: egressPolicyName}, &pol)
	if err != nil {
		return ""
	}
	if cni, ok := r.PolicyEnforcement(ctx); ok {
		return fmt.Sprintf("enforced by %s", cni)
	}
	return "NOT ENFORCED — no NetworkPolicy-capable CNI found"
}
