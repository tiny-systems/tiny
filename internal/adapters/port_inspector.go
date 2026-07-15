package adapters

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tiny-systems/tiny/internal/kube"
)

// PortInspector implements sdktools.PortInspector. It reads a node's
// reconciled port schema and enriches it with any configurable overlays
// set on the node's own _settings port, so callers can see the actual
// shape of user-configured fields like context.
//
// This is narrower than the platform's recursive-simulation inspector
// (which walks the entire graph). We only apply the node's OWN settings
// as an overlay on its OWN ports. Full cross-node propagation requires
// Fix 2 (publishing component schemas to TinyModule.Status) or
// scenario-based validation, both of which land separately.
//
// traceID is accepted but ignored — trace-based inspection is not
// implemented in v0.1.0.
type PortInspector struct {
	kube *kube.Client
}

func NewPortInspector(k *kube.Client) *PortInspector {
	return &PortInspector{kube: k}
}

func (p *PortInspector) InspectPort(ctx context.Context, projectName, nodeID, portName, traceID string) (*sdktools.PortInspectResult, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node id required")
	}
	if portName == "" {
		return nil, fmt.Errorf("port name required")
	}

	node := &v1alpha1.TinyNode{}
	if err := p.kube.Client.Get(ctx, types.NamespacedName{
		Namespace: p.kube.Namespace,
		Name:      nodeID,
	}, node); err != nil {
		return nil, fmt.Errorf("get node %s: %w", nodeID, err)
	}

	for _, port := range node.Status.Ports {
		if port.Name != portName {
			continue
		}
		return p.buildInspectResult(nodeID, port, node)
	}

	return nil, fmt.Errorf("port %q not found on node %q", portName, nodeID)
}

func (p *PortInspector) buildInspectResult(nodeID string, port v1alpha1.TinyNodePortStatus, node *v1alpha1.TinyNode) (*sdktools.PortInspectResult, error) {
	result := &sdktools.PortInspectResult{
		NodeID:   nodeID,
		PortName: port.Name,
		PortType: portTypeString(port.Source),
	}

	if len(port.Schema) > 0 {
		schema := map[string]interface{}{}
		if err := json.Unmarshal(port.Schema, &schema); err != nil {
			return nil, fmt.Errorf("unmarshal port schema: %w", err)
		}

		// Enrich with the node's own _settings overlay. If the user set
		// `context` or similar configurable fields via configure_node_settings,
		// surface those here so the caller can see the concrete shape.
		if overlay := loadSettingsOverlay(node); overlay != nil {
			applyOverlayToSchema(schema, overlay)
		}
		result.Schema = schema
		result.Configurable = hasConfigurableRoots(schema)
	}

	if len(port.Configuration) > 0 {
		var example interface{}
		if err := json.Unmarshal(port.Configuration, &example); err != nil {
			return nil, fmt.Errorf("unmarshal port configuration: %w", err)
		}
		result.ExampleData = example
	}

	return result, nil
}

func portTypeString(source bool) string {
	if source {
		return "source"
	}
	return "target"
}

// loadSettingsOverlay returns the parsed contents of the node's
// _settings port Schema field. This is the user-provided
// settings_schema from configure_node_settings (or from build_flow's
// settings_schema argument). Returns nil if unset.
func loadSettingsOverlay(node *v1alpha1.TinyNode) map[string]interface{} {
	for _, p := range node.Spec.Ports {
		if p.Port != v1alpha1.SettingsPort || len(p.Schema) == 0 {
			continue
		}
		overlay := map[string]interface{}{}
		if err := json.Unmarshal(p.Schema, &overlay); err != nil {
			return nil
		}
		return overlay
	}
	return nil
}

// applyOverlayToSchema walks schema and replaces any $defs entry whose
// "path" field matches a key in overlay with the overlay's shape.
//
// The overlay is shaped like:
//
//	{"context": {"type": "object", "properties": {"slackToken": {"type":"string"}, ...}}}
//
// The schema's $defs contains entries like:
//
//	"Startcontext": {"configurable": true, "path": "$.context", ...}
//
// For each overlay key, we look for a definition whose path ends with
// "." + key (e.g. "$.context") and splice the overlay's properties in
// while keeping the definition's own metadata (path, configurable).
func applyOverlayToSchema(schema, overlay map[string]interface{}) {
	defs, ok := schema["$defs"].(map[string]interface{})
	if !ok {
		return
	}

	for overlayKey, overlayVal := range overlay {
		overlayMap, ok := overlayVal.(map[string]interface{})
		if !ok {
			continue
		}
		// Find the def whose path ends with ".<overlayKey>"
		wantedSuffix := "." + overlayKey
		for _, def := range defs {
			defMap, ok := def.(map[string]interface{})
			if !ok {
				continue
			}
			path, _ := defMap["path"].(string)
			if path == "" {
				continue
			}
			if path == "$"+wantedSuffix || endsWith(path, wantedSuffix) {
				mergeSchemaShape(defMap, overlayMap)
				break
			}
		}
	}
}

// mergeSchemaShape copies structural fields (type, properties, items,
// required) from src onto dst, preserving dst's metadata (path,
// configurable, title, description).
func mergeSchemaShape(dst, src map[string]interface{}) {
	for _, key := range []string{"type", "properties", "items", "required", "enum", "format"} {
		if v, ok := src[key]; ok {
			dst[key] = v
		}
	}
}

// hasConfigurableRoots returns true if any $def in the schema is marked
// configurable (useful hint to the caller).
func hasConfigurableRoots(schema map[string]interface{}) bool {
	defs, ok := schema["$defs"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, def := range defs {
		if m, ok := def.(map[string]interface{}); ok {
			if b, _ := m["configurable"].(bool); b {
				return true
			}
		}
	}
	return false
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

var _ sdktools.PortInspector = (*PortInspector)(nil)
