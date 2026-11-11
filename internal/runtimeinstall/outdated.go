package runtimeinstall

import (
	"bytes"
	"context"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/tiny-systems/tiny/config"
	"github.com/tiny-systems/tiny/internal/kube"
)

const sessionsCRD = "sessions.agents.tinysystems.io"

var crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// MissingFields lists Session spec fields this binary knows and the
// cluster's CRD doesn't. The API server drops those fields without an
// error, so an outdated runtime makes newer flags silently do nothing.
// Creates refuse that case on their own (see workload.RefuseIfPruned);
// this is the early warning, shown before anything is attempted.
//
// It returns nil whenever it can't tell: CRDs are cluster-scoped and many
// namespace users may not read them, and a warning built on a guess is
// worse than none.
func MissingFields(ctx context.Context, k *kube.Client) []string {
	want := embeddedSpecFields()
	if len(want) == 0 {
		return nil
	}
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(crdGVK)
	if err := k.Client.Get(ctx, types.NamespacedName{Name: sessionsCRD}, crd); err != nil {
		return nil
	}
	have := specFields(crd.Object)
	if len(have) == 0 {
		return nil
	}
	var missing []string
	for _, f := range want {
		if !slices.Contains(have, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

func embeddedSpecFields() []string {
	raw, err := config.Manifests.ReadFile("crd/bases/agents.tinysystems.io_sessions.yaml")
	if err != nil {
		return nil
	}
	obj := map[string]any{}
	if err := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&obj); err != nil {
		return nil
	}
	return specFields(obj)
}

// specFields reads spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.
func specFields(obj map[string]any) []string {
	versions, _, _ := unstructured.NestedSlice(obj, "spec", "versions")
	if len(versions) == 0 {
		return nil
	}
	v, ok := versions[0].(map[string]any)
	if !ok {
		return nil
	}
	props, _, _ := unstructured.NestedMap(v, "schema", "openAPIV3Schema", "properties", "spec", "properties")
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
