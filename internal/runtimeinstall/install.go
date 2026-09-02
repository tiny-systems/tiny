/*
Package runtimeinstall makes `tiny new` self-sufficient: the session runtime
(two CRDs and the sidecar's ServiceAccount — no pods) travels inside the
binary and is applied on first contact, so the first command a person
learns is the one that does their work.

The manifests are embedded straight from this repo's config/ — the same
files the controller is developed against, so client and runtime cannot
drift apart.
*/
package runtimeinstall

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/config"
	"github.com/tiny-systems/tiny/internal/kube"
)

// Installed reports whether the runtime is already present — listing
// Sessions proves the CRD is served. Only a missing kind means "not
// installed"; RBAC denials and transient errors must not trigger a
// surprise re-apply.
func Installed(ctx context.Context, k *kube.Client) bool {
	list := &agentsv1.SessionList{}
	err := k.Client.List(ctx, list, client.InNamespace(k.Namespace))
	if err == nil {
		return true
	}
	return !meta.IsNoMatchError(err) && !apierrors.IsNotFound(err)
}

// Apply installs everything embedded: cluster-scoped CRDs as-is, namespaced
// objects into the client's namespace. Server-side apply, so re-running is a
// no-op and upgrades are the same verb as installs.
func Apply(ctx context.Context, restCfg *rest.Config, namespace string) error {
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	disco, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disco))

	var docs []*unstructured.Unstructured
	err = fs.WalkDir(config.Manifests, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := config.Manifests.ReadFile(path)
		if err != nil {
			return err
		}
		for doc := range bytes.SplitSeq(raw, []byte("\n---")) {
			if len(bytes.TrimSpace(doc)) == 0 {
				continue
			}
			obj := &unstructured.Unstructured{}
			if err := k8syaml.Unmarshal(doc, &obj.Object); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			if obj.GetKind() == "" {
				continue
			}
			docs = append(docs, obj)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// CRDs first, then everything else — the mapper cannot map a kind whose
	// definition is not established yet.
	var crds, others []*unstructured.Unstructured
	for _, o := range docs {
		if o.GetKind() == "CustomResourceDefinition" {
			crds = append(crds, o)
		} else {
			others = append(others, o)
		}
	}
	// The namespace itself first — a freshly picked "new namespace" only
	// exists as intent until here.
	nsObj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": namespace},
	}}
	if err := apply(ctx, dyn, mapper, nsObj, ""); err != nil {
		return err
	}
	for _, o := range crds {
		if err := apply(ctx, dyn, mapper, o, ""); err != nil {
			return err
		}
	}
	mapper.Reset()
	for _, o := range others {
		if err := apply(ctx, dyn, mapper, o, namespace); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, dyn dynamic.Interface, mapper meta.RESTMapper, obj *unstructured.Unstructured, namespace string) error {
	gvk := obj.GroupVersionKind()
	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	if err != nil {
		return fmt.Errorf("map %s: %w", gvk.Kind, err)
	}
	var iface dynamic.ResourceInterface = dyn.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := namespace
		if ns == "" {
			ns = "default"
		}
		obj.SetNamespace(ns)
		iface = dyn.Resource(mapping.Resource).Namespace(ns)
	}
	data, err := obj.MarshalJSON()
	if err != nil {
		return err
	}
	_, err = iface.Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
		FieldManager: "tiny",
		Force:        ptrBool(true),
	})
	if err != nil {
		return fmt.Errorf("apply %s/%s: %w", strings.ToLower(gvk.Kind), obj.GetName(), err)
	}
	return nil
}

func ptrBool(b bool) *bool { return &b }
