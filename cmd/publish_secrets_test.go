package cmd

import (
	"strings"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
)

// The shape actually found in a live project: a user-supplied key, entered
// through a widget exactly as intended, captured back into a scenario sample
// by a key-value node's query_result.
func TestRedactScenarioPorts_MasksCapturedKey(t *testing.T) {
	key := "sk-ant-api03-" + strings.Repeat("A1b2C3d4", 11)
	ports := []v1alpha1.ScenarioPortData{{
		Port: "kv-2ff37:query_result",
		Data: []byte(`{"context":{"id":7},"value":"` + key + `"}`),
	}}

	out, touched := redactScenarioPorts(ports)
	if len(touched) != 1 || touched[0] != "kv-2ff37:query_result" {
		t.Fatalf("touched = %v, want the one port", touched)
	}
	body := string(out[0].Data)
	if strings.Contains(body, key) {
		t.Fatal("the key survived into the exported sample")
	}
	if !strings.Contains(body, redactedSample) {
		t.Fatalf("expected a redaction marker, got %s", body)
	}
	// The sample's job is to pin the port's SHAPE, so the structure around
	// the secret has to survive.
	if !strings.Contains(body, `"context"`) || !strings.Contains(body, `"value"`) {
		t.Fatalf("sample shape was damaged: %s", body)
	}
}

// A secret embedded in free text — a log line, a rendered prompt — is the
// other way one reaches a sample, and it does not sit in a field of its own.
func TestRedactScenarioPorts_MasksInsideFreeText(t *testing.T) {
	ports := []v1alpha1.ScenarioPortData{{
		Port: "pod-logs-get-71601:logs",
		Data: []byte(`{"logs":"level=info msg=\"calling api\" token=ghp_` + strings.Repeat("z", 30) + `"}`),
	}}

	out, touched := redactScenarioPorts(ports)
	if len(touched) != 1 {
		t.Fatalf("touched = %v, want one", touched)
	}
	if strings.Contains(string(out[0].Data), "ghp_zzzz") {
		t.Fatalf("token survived: %s", out[0].Data)
	}
}

// Ordinary samples must round-trip untouched — a false positive silently
// corrupts data someone's edge validation depends on.
func TestRedactScenarioPorts_LeavesOrdinaryDataAlone(t *testing.T) {
	ports := []v1alpha1.ScenarioPortData{
		{Port: "a:in", Data: []byte(`{"name":"broken-checkout-657f5f7dd-6mz5c","restarts":0}`)},
		{Port: "b:in", Data: []byte(`{"id":"3802e363-9bc2-11f1-9d40-763d6350ade8"}`)},
		{Port: "c:in", Data: []byte(`{"sha":"b83f5420419d8af18f2c2b66090520f7aff145bb"}`)},
	}

	out, touched := redactScenarioPorts(ports)
	if len(touched) != 0 {
		t.Fatalf("touched = %v, want none — pod names, UUIDs and commit shas are not secrets", touched)
	}
	for i := range ports {
		if string(out[i].Data) != string(ports[i].Data) {
			t.Fatalf("port %s was rewritten: %s", ports[i].Port, out[i].Data)
		}
	}
}

// A sample that is not JSON still must not carry a credential out.
func TestRedactScenarioPorts_ScrubsNonJSON(t *testing.T) {
	ports := []v1alpha1.ScenarioPortData{{
		Port: "x:out",
		Data: []byte("Authorization: Bearer " + strings.Repeat("k", 40)),
	}}

	out, touched := redactScenarioPorts(ports)
	if len(touched) != 1 {
		t.Fatalf("touched = %v, want one", touched)
	}
	if strings.Contains(string(out[0].Data), strings.Repeat("k", 40)) {
		t.Fatalf("bearer token survived: %s", out[0].Data)
	}
}

// A log line usually carries a TRUNCATED key rather than a whole one. The
// fragment is not usable on its own, but it is still the leading portion of a
// real credential, so it must not be exported either. This is the exact shape
// found in a live pod-logs sample.
func TestRedactScenarioPorts_MasksTruncatedKey(t *testing.T) {
	ports := []v1alpha1.ScenarioPortData{{
		Port: "pod-logs-get-71601:logs",
		Data: []byte(`{"logs":"using key sk-ant-api03-Ab… (truncated)"}`),
	}}

	out, touched := redactScenarioPorts(ports)
	if len(touched) != 1 {
		t.Fatalf("touched = %v, want the log sample", touched)
	}
	if strings.Contains(string(out[0].Data), "sk-ant-") {
		t.Fatalf("key prefix survived: %s", out[0].Data)
	}
}
