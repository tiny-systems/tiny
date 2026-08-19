package cmd

import (
	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/redact"
)

// Scenario samples are captured from real traffic, so a credential the user
// supplied at runtime can be sitting in one — and unlike graph elements,
// samples were published verbatim.
//
// Redaction by key name cannot help: the value lands inside an ordinary
// payload field (a key-value store's query_result, a log line) whose name
// says nothing about what it holds. redact.JSONByShape matches the shape of
// the secret instead; the platform's import gate uses the same matcher, so
// the client-side pass and the server-side one cannot drift apart.
//
// Samples exist to pin the SHAPE of a port's data for compile-time edge
// validation, never its contents, so replacing a matched value costs the
// sample nothing.

// redactScenarioPorts masks credentials in scenario sample data before it is
// exported, returning the ports to publish and the port names that were
// touched so the caller can say what it did.
func redactScenarioPorts(ports []v1alpha1.ScenarioPortData) ([]v1alpha1.ScenarioPortData, []string) {
	var touched []string
	out := make([]v1alpha1.ScenarioPortData, 0, len(ports))

	for _, p := range ports {
		scrubbed, redacted := redact.JSONByShape(p.Data)
		if !redacted {
			out = append(out, p)
			continue
		}
		touched = append(touched, p.Port)
		if scrubbed == nil {
			// Known to contain a secret and not re-encodable: drop the sample
			// rather than publish the original.
			continue
		}
		p.Data = scrubbed
		out = append(out, p)
	}
	return out, touched
}
