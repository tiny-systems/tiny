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
not hostnames. Data can still leave over 443, and over DNS — port 53 is
deliberately unrestricted so that clusters resolving through NodeLocal
DNSCache at 169.254.20.10 are not broken by the link-local exclusion. Narrowing that needs an
egress proxy the policy points at, which is a later step.

In-namespace traffic stays allowed on purpose: sessions hand each other
files through the artifact store and reach each other's exposed ports.
*/
package addons

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
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
const (
	// cidrLinkLocal carries the cloud metadata endpoint at 169.254.169.254
	// and, on some clusters, NodeLocal DNSCache at 169.254.20.10.
	cidrLinkLocal = "169.254.0.0/16"
	cidrAnywhere  = "0.0.0.0/0"
)

var privateRanges = []string{
	cidrLinkLocal,
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
//
// viaProxy narrows it further: with the egress proxy running, sessions
// have no internet rule at all. Everything outbound goes through the
// proxy, which is in-namespace, and is filtered there BY HOSTNAME —
// which is the thing a NetworkPolicy fundamentally cannot do.
func (r *Applier) EnsureEgressPolicy(ctx context.Context, ns string, viaProxy bool) error {
	dnsUDP := corev1.ProtocolUDP
	dnsPort := intstr.FromInt32(53)

	internet := []networkingv1.NetworkPolicyPeer{{
		IPBlock: &networkingv1.IPBlock{CIDR: cidrAnywhere, Except: privateRanges},
	}}
	apiPeers, apiPorts := r.apiServerPeers(ctx)

	pol := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: egressPolicyName},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "tiny-session"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      egressRules(viaProxy, internet, dnsUDP, dnsPort, apiPeers, apiPorts),
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

// egressRules is the difference the proxy makes.
//
// Without it, DNS goes anywhere and the internet is reachable on 80/443
// with private space cut out — the metadata endpoint is closed, but any
// host is not.
//
// With it, the internet rule disappears entirely and DNS narrows to the
// cluster's own resolvers: kube-system, plus link-local on port 53 ONLY,
// which is where NodeLocal DNSCache listens. Port 53 to 169.254.0.0/16
// does not reopen the metadata endpoint, which answers on 80 and 443.
// Queries to a nameserver an attacker controls stop being reachable
// directly, which is the DNS tunnel closed.
func egressRules(viaProxy bool, internet []networkingv1.NetworkPolicyPeer, dnsUDP corev1.Protocol, dnsPort intstr.IntOrString, apiPeers []networkingv1.NetworkPolicyPeer, apiPorts []networkingv1.NetworkPolicyPort) []networkingv1.NetworkPolicyEgressRule {
	dnsPorts := []networkingv1.NetworkPolicyPort{{Protocol: &dnsUDP, Port: &dnsPort}, tcp(53)}
	// This namespace: the artifact store, each other's exposed ports, and
	// the proxy itself. Sessions collaborating is a feature.
	inNamespace := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
	}
	// The API server, always: an agent that cannot read its own spec.inbox
	// never receives the work delivered to it.
	apiRule := networkingv1.NetworkPolicyEgressRule{To: apiPeers, Ports: apiPorts}
	if !viaProxy {
		out := []networkingv1.NetworkPolicyEgressRule{
			{Ports: dnsPorts},
			inNamespace,
			{To: internet, Ports: []networkingv1.NetworkPolicyPort{tcp(80), tcp(443)}},
		}
		if len(apiPeers) > 0 {
			out = append(out, apiRule)
		}
		return out
	}
	viaProxyRules := []networkingv1.NetworkPolicyEgressRule{
		{Ports: dnsPorts, To: []networkingv1.NetworkPolicyPeer{
			{NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": nsKubeSystem},
			}},
			{IPBlock: &networkingv1.IPBlock{CIDR: cidrLinkLocal}},
		}},
		inNamespace,
	}
	if len(apiPeers) > 0 {
		viaProxyRules = append(viaProxyRules, apiRule)
	}
	return viaProxyRules
}

// apiServerPeers allows the pod to reach the Kubernetes API. It has to be
// discovered, not hardcoded: the Service ClusterIP differs per cluster
// (10.96.0.1 on kubeadm, 10.43.0.1 on k3s) and the real endpoint behind it
// is usually a node address, which the private-range exclusion would
// otherwise cut.
//
// Without this an agent cannot read its own spec.inbox, so delivered work
// never reaches it and the session sits idle looking healthy.
func (r *Applier) apiServerPeers(ctx context.Context) ([]networkingv1.NetworkPolicyPeer, []networkingv1.NetworkPolicyPort) {
	var peers []networkingv1.NetworkPolicyPeer
	var ports []networkingv1.NetworkPolicyPort
	seenPort := map[int32]bool{}

	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: nsDefault, Name: svcKubernetes}, &svc); err == nil {
		if ip := svc.Spec.ClusterIP; ip != "" && ip != "None" {
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: ip + "/32"},
			})
		}
		for _, p := range svc.Spec.Ports {
			if !seenPort[p.Port] {
				seenPort[p.Port] = true
				ports = append(ports, tcp(p.Port))
			}
		}
	}
	// The endpoints behind it: most CNIs evaluate egress after DNAT, so the
	// ClusterIP alone is not enough. EndpointSlice, not the deprecated
	// Endpoints, which is on its way out of the API.
	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(nsDefault),
		client.MatchingLabels{discoveryv1.LabelServiceName: svcKubernetes}); err == nil {
		for _, sl := range slices.Items {
			for _, ep := range sl.Endpoints {
				for _, addr := range ep.Addresses {
					peers = append(peers, networkingv1.NetworkPolicyPeer{
						IPBlock: &networkingv1.IPBlock{CIDR: addr + "/32"},
					})
				}
			}
			for _, p := range sl.Ports {
				if p.Port != nil && !seenPort[*p.Port] {
					seenPort[*p.Port] = true
					ports = append(ports, tcp(*p.Port))
				}
			}
		}
	}
	return peers, ports
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
}

// cniK3s is separate because k3s enforces NetworkPolicy from a
// kube-router controller embedded in the k3s process itself. There is no
// DaemonSet to find, so looking for one reports NOT ENFORCED on a
// cluster that is in fact enforcing.
//
// The EKS and AKS CNIs are deliberately absent for the opposite reason:
// their DaemonSets are present whether or not policy enforcement is
// switched on, so finding one proves nothing. Claiming enforcement that
// is not happening is the worse error of the two.
const (
	cniK3s        = "k3s (embedded kube-router)"
	nsKubeSystem  = "kube-system"
	nsDefault     = "default"
	svcKubernetes = "kubernetes"
)

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
	// k3s first: it enforces without a DaemonSet to look for.
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err == nil {
		for _, n := range nodes.Items {
			if strings.Contains(n.Status.NodeInfo.KubeletVersion, "+k3s") {
				return cniK3s, true
			}
		}
	}

	var dss appsv1.DaemonSetList
	if err := r.List(ctx, &dss, client.InNamespace(nsKubeSystem)); err != nil {
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
