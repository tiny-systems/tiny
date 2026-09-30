package sessions

// A new CLI ships new add-on specs, and with no manager running nothing
// rolls them out. tiny init re-applies whatever the switchboard says is
// on, so an upgrade reaches the proxy, the web page and the runner
// without anyone toggling a checkbox off and on.

import (
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/addons"
	"github.com/tiny-systems/tiny/internal/kube"
)

func TestReapplyAddonsUpgradesWhatIsAlreadyOn(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme,
		discoveryv1.AddToScheme, rbacv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	one := int32(1)
	stale := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: addons.ProxyName},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": addons.ProxyName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": addons.ProxyName}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "proxy", Image: "old", Args: []string{"proxy", "--addr=:3128"},
				}}},
			},
		},
	}
	staleSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: addons.ProxyName},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.9",
			Ports:     []corev1.ServicePort{{Name: "proxy", Port: 3128}},
		},
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: settingsCM},
		Data:       map[string]string{keyEgress: trueWord, keyEgressProxy: trueWord},
	}
	fc := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale, staleSvc, cm).Build()
	s := &Store{Kube: &kube.Client{Client: fc, Namespace: "agents"}}
	if err := s.ReapplyAddons(t.Context()); err != nil {
		t.Fatal(err)
	}

	var dep appsv1.Deployment
	if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: addons.ProxyName}, &dep); err != nil {
		t.Fatal(err)
	}
	args := dep.Spec.Template.Spec.Containers[0].Args
	if !slices.ContainsFunc(args, func(a string) bool { return a == "--dns=:5353" }) {
		t.Fatalf("proxy still runs the old spec after re-apply: %v", args)
	}
	var svc corev1.Service
	if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: addons.ProxyName}, &svc); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.ClusterIP != "10.0.0.9" {
		t.Fatal("re-apply changed the proxy's ClusterIP — every session's HTTPS_PROXY and resolver point at it")
	}
	if !slices.ContainsFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return p.Port == 53 }) {
		t.Fatalf("service ports not upgraded: %v", svc.Spec.Ports)
	}
	var pol networkingv1.NetworkPolicy
	if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: "tiny-session-egress"}, &pol); err != nil {
		t.Fatalf("egress policy not applied on re-apply: %v", err)
	}
}
