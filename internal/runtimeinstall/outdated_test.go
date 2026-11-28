package runtimeinstall

import (
	"context"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/kube"
)

func crdWith(fields ...string) *unstructured.Unstructured {
	props, status := map[string]any{}, map[string]any{}
	for _, f := range fields {
		if name, ok := strings.CutPrefix(f, "status."); ok {
			status[name] = map[string]any{"type": "string"}
			continue
		}
		props[f] = map[string]any{"type": "string"}
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"versions": []any{map[string]any{
			"name": "v1alpha1",
			"schema": map[string]any{"openAPIV3Schema": map[string]any{"properties": map[string]any{
				"spec":   map[string]any{"properties": props},
				"status": map[string]any{"properties": status},
			}}},
		}}},
	}}
	u.SetGroupVersionKind(crdGVK)
	u.SetName(sessionsCRD)
	return u
}

func fakeKube(objs ...*unstructured.Unstructured) *kube.Client {
	b := clientfake.NewClientBuilder().WithScheme(runtime.NewScheme())
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	return &kube.Client{Client: b.Build(), Namespace: "agents"}
}

// A cluster installed before runtimeClassName existed must be reported, so
// the user upgrades before --runtime-class silently does nothing.
func TestMissingFieldsNamesWhatAnOldClusterLacks(t *testing.T) {
	missing := MissingFields(context.Background(), fakeKube(crdWith("task", "repo", "image")))
	if !slices.Contains(missing, "runtimeClassName") || !slices.Contains(missing, "mcpServers") {
		t.Fatalf("missing = %v, want runtimeClassName and mcpServers among them", missing)
	}
	if !slices.Contains(missing, "status.shown") {
		t.Fatalf("missing = %v, want status.shown (show_user) among them", missing)
	}
	if slices.Contains(missing, "task") {
		t.Fatal("a field the cluster has was reported missing")
	}
}

func TestUpToDateClusterReportsNothing(t *testing.T) {
	if m := MissingFields(context.Background(), fakeKube(crdWith(embeddedSpecFields()...))); len(m) != 0 {
		t.Fatalf("current CRD reported missing %v", m)
	}
}

// Can't read the CRD (absent, or no RBAC for cluster-scoped objects): say
// nothing rather than guess.
func TestUnreadableCRDStaysQuiet(t *testing.T) {
	if m := MissingFields(context.Background(), fakeKube()); m != nil {
		t.Fatalf("no CRD visible, but reported %v", m)
	}
}

func TestEmbeddedCRDHasTheNewestFields(t *testing.T) {
	f := embeddedSpecFields()
	if !slices.Contains(f, "runtimeClassName") || !slices.Contains(f, "task") {
		t.Fatalf("embedded spec fields = %v", f)
	}
}
