package workload

// A session created while its namesake's workspace PVC was still
// terminating adopted the dying claim; GC swept it and the pod parked
// Pending on a claim that never returned. ensureWorkspace must refuse
// both a terminating claim and one owned by a different session.

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func workspaceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := agentsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func testSession(uid types.UID) *agentsv1.Session {
	return &agentsv1.Session{ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "ns", UID: uid}}
}

func TestEnsureWorkspaceRefusesTerminatingClaim(t *testing.T) {
	scheme := workspaceScheme(t)
	now := metav1.Now()
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "s1-workspace", Namespace: "ns",
		DeletionTimestamp: &now, Finalizers: []string{"kubernetes.io/pvc-protection"},
	}}
	c := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()

	err := ensureWorkspace(t.Context(), c, testSession("uid-new"))
	if err == nil || !strings.Contains(err.Error(), "still being deleted") {
		t.Fatalf("want loud terminating-claim error, got %v", err)
	}
}

func TestEnsureWorkspaceRefusesForeignClaim(t *testing.T) {
	scheme := workspaceScheme(t)
	old := testSession("uid-old")
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "s1-workspace", Namespace: "ns"}}
	if err := controllerutil.SetControllerReference(old, pvc, scheme); err != nil {
		t.Fatal(err)
	}
	c := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()

	err := ensureWorkspace(t.Context(), c, testSession("uid-new"))
	if err == nil || !strings.Contains(err.Error(), "earlier session") {
		t.Fatalf("want foreign-owner error, got %v", err)
	}
}

func TestEnsureWorkspaceAcceptsOwnClaimAndCreatesFresh(t *testing.T) {
	scheme := workspaceScheme(t)
	se := testSession("uid-1")
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "s1-workspace", Namespace: "ns"}}
	if err := controllerutil.SetControllerReference(se, pvc, scheme); err != nil {
		t.Fatal(err)
	}
	c := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
	if err := ensureWorkspace(t.Context(), c, se); err != nil {
		t.Fatalf("own claim must be accepted: %v", err)
	}

	// And with nothing there, a fresh claim is created and owned.
	c2 := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	if err := ensureWorkspace(t.Context(), c2, se); err != nil {
		t.Fatalf("fresh create: %v", err)
	}
	got := &corev1.PersistentVolumeClaim{}
	if err := c2.Get(t.Context(), types.NamespacedName{Namespace: "ns", Name: "s1-workspace"}, got); err != nil {
		t.Fatal(err)
	}
	if owner := metav1.GetControllerOf(got); owner == nil || owner.UID != se.UID {
		t.Fatalf("fresh claim not owned by session: %+v", got.OwnerReferences)
	}
}
