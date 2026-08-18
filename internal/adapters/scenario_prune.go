package adapters

import (
	"context"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdkutils "github.com/tiny-systems/module/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// pruneOrphanScenarioPorts drops scenario samples whose node no longer exists.
//
// Scenario CRs carry no owner references and no controller, so nothing
// collects them: deleting a node left its samples behind forever, and every
// edge edit re-scaffolds more. At runtime that is inert — samples are looked
// up by exact live port key, so a key naming a dead node is never read — but
// `tiny publish` refuses to export a project whose samples reference missing
// nodes, and the remedy it prints is not one a user can act on: orphans
// collect in the machine-written auto-scaffold scenario, and the only delete
// available destroys the whole scenario.
//
// Written as a sweep rather than a targeted delete so it also heals projects
// that already accumulated orphans, and so a partially-failed delete cannot
// leave a sample stranded.
//
// Best-effort by contract: callers run it after a delete that has already
// succeeded, and a failure to tidy must not turn that into a reported error.
// It returns the number of samples removed so callers can log or surface it.
func pruneOrphanScenarioPorts(ctx context.Context, kc *kube.Client, projectName string) (int, error) {
	if projectName == "" {
		return 0, nil
	}

	nodes := &v1alpha1.TinyNodeList{}
	if err := kc.Client.List(ctx, nodes,
		client.InNamespace(kc.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	); err != nil {
		return 0, fmt.Errorf("list nodes: %w", err)
	}
	live := make(map[string]bool, len(nodes.Items))
	for _, n := range nodes.Items {
		live[n.Name] = true
	}

	scenarios := &v1alpha1.TinyScenarioList{}
	if err := kc.Client.List(ctx, scenarios,
		client.InNamespace(kc.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	); err != nil {
		return 0, fmt.Errorf("list scenarios: %w", err)
	}

	removed := 0
	for i := range scenarios.Items {
		scenario := &scenarios.Items[i]
		kept := make([]v1alpha1.ScenarioPortData, 0, len(scenario.Spec.Ports))
		for _, port := range scenario.Spec.Ports {
			nodeName, _ := sdkutils.ParseFullPortName(port.Port)
			if live[nodeName] {
				kept = append(kept, port)
				continue
			}
			removed++
		}
		if len(kept) == len(scenario.Spec.Ports) {
			continue
		}
		scenario.Spec.Ports = kept
		if err := kc.Client.Update(ctx, scenario); err != nil {
			return removed, fmt.Errorf("update scenario %s: %w", scenario.Name, err)
		}
	}
	return removed, nil
}
