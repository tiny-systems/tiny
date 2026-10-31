package session_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/pkg/session"
)

func fakeClient(t *testing.T) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, agentsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	// The fake client assigns no UIDs; the API server does. Owner
	// references need one, so stand in for the server here.
	n := 0
	return clientfake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetUID() == "" {
				n++
				obj.SetUID(types.UID(fmt.Sprintf("uid-%d", n)))
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
}

func newSession() *agentsv1.Session {
	return &agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "s1"},
		Spec: agentsv1.SessionSpec{
			Task:       "do the thing",
			MCPServers: []agentsv1.MCPServer{{Name: "bp", URL: "http://director.bp.svc:8080/mcp"}},
		},
	}
}

func TestCreateMakesSessionWorkspaceAndDeployment(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	if err := session.Create(ctx, c, newSession()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := &agentsv1.Session{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1"}, got); err != nil {
		t.Fatalf("session: %v", err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1-workspace"}, pvc); err != nil {
		t.Fatalf("workspace PVC: %v", err)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1-agent"}, dep); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	for _, obj := range []metav1.Object{pvc, dep} {
		if owner := metav1.GetControllerOf(obj); owner == nil || owner.UID != got.UID {
			t.Errorf("%s not owned by the session: %+v", obj.GetName(), obj.GetOwnerReferences())
		}
	}
	if dep.Spec.Template.Labels[session.SessionLabel] != "s1" {
		t.Errorf("pod template labels = %v, want %s=s1", dep.Spec.Template.Labels, session.SessionLabel)
	}
}

func countWorkload(t *testing.T, c client.Client) (int, int) {
	t.Helper()
	ctx := t.Context()
	deps := &appsv1.DeploymentList{}
	if err := c.List(ctx, deps, client.InNamespace("ns")); err != nil {
		t.Fatal(err)
	}
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := c.List(ctx, pvcs, client.InNamespace("ns")); err != nil {
		t.Fatal(err)
	}
	return len(deps.Items), len(pvcs.Items)
}

func TestCreateRefusesExistingSession(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	// A foreign Session, created by someone else: no workload yet.
	foreign := newSession()
	foreign.Spec.Task = "someone else's task"
	if err := c.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	err := session.Create(ctx, c, newSession())
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("Create on existing name = %v, want AlreadyExists", err)
	}
	if d, p := countWorkload(t, c); d != 0 || p != 0 {
		t.Fatalf("workload created for a foreign session: %d deployments, %d PVCs", d, p)
	}
	got := &agentsv1.Session{}
	err = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1"}, got)
	if err != nil || got.Spec.Task != "someone else's task" {
		t.Fatalf("foreign session changed: %v %+v", err, got.Spec)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	if err := session.Create(ctx, c, newSession()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := session.Ensure(ctx, c, newSession()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if d, p := countWorkload(t, c); d != 1 || p != 1 {
		t.Fatalf("after Create+Ensure: %d deployments, %d PVCs, want 1 and 1", d, p)
	}
}

func forbidGet(t *testing.T, resource string) client.Client {
	t.Helper()
	base := fakeClient(t)
	return interceptor.NewClient(base, interceptor.Funcs{
		Get: func(
			ctx context.Context, c client.WithWatch,
			key client.ObjectKey, obj client.Object, opts ...client.GetOption,
		) error {
			switch obj.(type) {
			case *corev1.ConfigMap:
				if resource == "configmaps" {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, errors.New("no"))
				}
			case *corev1.Service:
				if resource == "services" {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, key.Name, errors.New("no"))
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

func TestEnsureFailsLoudWhenSettingsForbidden(t *testing.T) {
	c := forbidGet(t, "configmaps")
	ctx := t.Context()
	err := session.Create(ctx, c, newSession())
	if err == nil || !strings.Contains(err.Error(), "configmap") || !apierrors.IsForbidden(err) {
		t.Fatalf("Create = %v, want a Forbidden error naming the configmap", err)
	}
	if d, _ := countWorkload(t, c); d != 0 {
		t.Fatalf("deployment created despite the error")
	}
}

func TestEnsureFailsLoudWhenEgressServiceForbidden(t *testing.T) {
	c := forbidGet(t, "services")
	ctx := t.Context()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: settings.Name},
		Data:       map[string]string{"egressProxy": "true"},
	}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	err := session.Create(ctx, c, newSession())
	if err == nil || !strings.Contains(err.Error(), "service") || !apierrors.IsForbidden(err) {
		t.Fatalf("Create = %v, want a Forbidden error naming the service", err)
	}
	if d, _ := countWorkload(t, c); d != 0 {
		t.Fatalf("deployment created despite the error")
	}
}

// NotFound is the add-on not being installed, not a failure: no
// ConfigMap, or the egress switch on with no Service yet.
func TestEnsureSucceedsWhenAddonsNotFound(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	if err := session.Create(ctx, c, newSession()); err != nil {
		t.Fatalf("no settings: %v", err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: settings.Name},
		Data:       map[string]string{"egressProxy": "true", "zot": "true"},
	}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	other := newSession()
	other.Name = "s2"
	if err := session.Create(ctx, c, other); err != nil {
		t.Fatalf("settings on, services missing: %v", err)
	}
}

// A session created by hand (kubectl apply) gets its workload from Ensure,
// which reads the UID the caller's copy does not have.
func TestEnsureFetchesUIDForUnreadSession(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	if err := c.Create(ctx, newSession()); err != nil {
		t.Fatal(err)
	}
	if err := session.Ensure(ctx, c, newSession()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dep := &appsv1.Deployment{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1-agent"}, dep); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if owner := metav1.GetControllerOf(dep); owner == nil || owner.UID == "" {
		t.Fatalf("deployment owner has no UID: %+v", dep.OwnerReferences)
	}
}

func TestDeleteIgnoresMissing(t *testing.T) {
	c := fakeClient(t)
	ctx := t.Context()
	if err := session.Create(ctx, c, newSession()); err != nil {
		t.Fatal(err)
	}
	if err := session.Delete(ctx, c, "ns", "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "s1"}, &agentsv1.Session{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("session still there after Delete: %v", err)
	}
	if err := session.Delete(ctx, c, "ns", "s1"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}
