/*
The egress proxy add-on: the hostname allow-list the NetworkPolicy points
at once it stops pointing at the internet.

It rides the controller image in `proxy` mode, so it costs no build of
its own. The list lives in a ConfigMap an operator can edit; the proxy
re-reads it, so widening it does not mean restarting anything.
*/
package addons

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	proxyName    = "tiny-egress"
	proxyArg     = "proxy"
	proxyAllowCM = "tiny-egress-allow"
	proxyPort    = 3128
	allowKey     = "hosts"
)

// DefaultAllowList is what a namespace starts with: the model APIs both
// agents need, and the package registries a coding session cannot work
// without. Deliberately short. Widening it is a decision someone should
// make on purpose, and every refusal names the host and says how.
const DefaultAllowList = `# One host per line. A leading dot matches subdomains.
# Edited live: the proxy re-reads this within a minute.

# The agents' own APIs. Removing these stops the sessions working.
api.anthropic.com
.anthropic.com
api.openai.com
.openai.com
chatgpt.com

# Source and packages.
github.com
.github.com
.githubusercontent.com
proxy.golang.org
sum.golang.org
registry.npmjs.org
.npmjs.org
pypi.org
.pypi.org
files.pythonhosted.org
crates.io
static.crates.io
`

// ProxyServiceIP is where sessions send their traffic. Returned so the
// workload can set HTTPS_PROXY to an address rather than a name, which
// means a session needs no DNS to reach it.
func (r *Applier) ProxyServiceIP(ctx context.Context, ns string) (string, error) {
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: proxyName}, &svc); err != nil {
		return "", err
	}
	return svc.Spec.ClusterIP, nil
}

// EnsureProxyAddon creates the allow-list, the Service and the proxy.
func (r *Applier) EnsureProxyAddon(ctx context.Context, ns, image string) error {
	if err := r.ensureAllowList(ctx, ns); err != nil {
		return err
	}
	if err := r.ensureProxyService(ctx, ns); err != nil {
		return err
	}
	return r.ensureProxyDeployment(ctx, ns, image)
}

// ensureAllowList seeds the list once and never overwrites it: after the
// first apply it belongs to whoever has been editing it.
func (r *Applier) ensureAllowList(ctx context.Context, ns string) error {
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: proxyAllowCM}, &cm)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: proxyAllowCM},
		Data:       map[string]string{allowKey: DefaultAllowList},
	})
}

func (r *Applier) ensureProxyService(ctx context.Context, ns string) error {
	var svc corev1.Service
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: proxyName}, &svc)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	return r.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: proxyName, Labels: map[string]string{appLabel: proxyName}},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{appLabel: proxyName},
			Ports:    []corev1.ServicePort{{Name: proxyArg, Port: proxyPort, TargetPort: intstr.FromInt32(proxyPort)}},
		},
	})
}

func (r *Applier) ensureProxyDeployment(ctx context.Context, ns, image string) error {
	one := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: proxyName, Labels: map[string]string{appLabel: proxyName}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: proxyName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{appLabel: proxyName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  proxyArg,
						Image: image,
						Args: []string{proxyArg,
							fmt.Sprintf("--addr=:%d", proxyPort),
							"--allow-file=/etc/tiny/" + allowKey,
						},
						Ports: []corev1.ContainerPort{{ContainerPort: proxyPort}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "allow", MountPath: "/etc/tiny",
						}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("32Mi"),
							},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "allow",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: proxyAllowCM},
							},
						},
					}},
				},
			},
		},
	}
	var existing appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: proxyName}, &existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, dep)
	}
	if err != nil {
		return err
	}
	existing.Spec = dep.Spec
	return r.Update(ctx, &existing)
}

// TeardownProxyAddon removes the proxy but KEEPS the allow-list: it is
// operator-edited, and deleting someone's curated list because they
// unticked a box for an afternoon would be rude.
func (r *Applier) TeardownProxyAddon(ctx context.Context, ns string) error {
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: proxyName}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: proxyName}},
	} {
		if err := r.deleteIfExists(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

// AllowedHosts reports the list as the settings screen shows it.
func (r *Applier) AllowedHosts(ctx context.Context, ns string) []string {
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: proxyAllowCM}, &cm); err != nil {
		return nil
	}
	var out []string
	for line := range strings.SplitSeq(cm.Data[allowKey], "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if h := strings.TrimSpace(line); h != "" {
			out = append(out, h)
		}
	}
	return out
}
