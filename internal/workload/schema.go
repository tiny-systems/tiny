package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// The API server prunes fields its CRD doesn't know, without an error. A
// newer CLI against an older install therefore drops new spec fields
// silently: --runtime-class gvisor would start a pod with no sandbox that
// still looks sandboxed to whoever asked for one. Every create compares
// what it sent with what the server stored and refuses on a difference.

// droppedFields names the top-level spec fields set in want that the
// server did not keep in got.
func droppedFields(want, got agentsv1.SessionSpec) []string {
	w, g := specKeys(want), specKeys(got)
	var out []string
	for k := range w {
		if _, ok := g[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func specKeys(s agentsv1.SessionSpec) map[string]json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(b, &m)
	return m
}

// RefuseIfPruned is called right after creating s, with want being the
// spec as sent. If the server dropped any field, the session is deleted
// before a workload exists and an error says how to fix the cluster.
func RefuseIfPruned(ctx context.Context, c client.Client, want agentsv1.SessionSpec, s *agentsv1.Session) error {
	dropped := droppedFields(want, s.Spec)
	if len(dropped) == 0 {
		return nil
	}
	_ = c.Delete(ctx, s)
	return fmt.Errorf("this cluster's Session CRD is older than this tiny and silently dropped: %s. "+
		"Nothing was started. Upgrade the CRD with `tiny init` (needs cluster-admin), then retry",
		strings.Join(dropped, ", "))
}
