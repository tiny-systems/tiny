package cmd

import (
	"strings"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func widgetNode(name string, settings string, incoming map[string]string) v1alpha1.TinyNode {
	n := v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{v1alpha1.DashboardLabel: "true"},
		},
	}
	if settings != "" {
		n.Spec.Ports = append(n.Spec.Ports, v1alpha1.TinyNodePortConfig{
			Port:          v1alpha1.SettingsPort,
			Configuration: []byte(settings),
		})
	}
	for port, cfg := range incoming {
		n.Spec.Ports = append(n.Spec.Ports, v1alpha1.TinyNodePortConfig{
			From:          "upstream:out",
			Port:          port,
			Configuration: []byte(cfg),
		})
	}
	return n
}

// A credential on the same form as per-run inputs means pasting a key on
// every run, and a secret in every message payload.
func TestRejectsCredentialMergedIntoRunForm(t *testing.T) {
	nodes := map[string]v1alpha1.TinyNode{
		"f.mod.signal-1": widgetNode("f.mod.signal-1",
			`{"context":{"apiKey":"","question":"why?","conversationId":"ops"}}`, nil),
	}
	err := checkWidgetShape(nodes)
	if err == nil {
		t.Fatal("merged credential form was accepted")
	}
	if !strings.Contains(err.Error(), "inject.config") {
		t.Errorf("error should point at the fix, got: %v", err)
	}
}

// A widget whose only field IS the credential is the correct shape.
func TestAcceptsDedicatedCredentialWidget(t *testing.T) {
	nodes := map[string]v1alpha1.TinyNode{
		"f.mod.signal-cfg": widgetNode("f.mod.signal-cfg", `{"context":{"apiKey":""}}`, nil),
		"f.mod.signal-ask": widgetNode("f.mod.signal-ask",
			`{"context":{"question":"why?","conversationId":"ops"}}`, nil),
	}
	if err := checkWidgetShape(nodes); err != nil {
		t.Errorf("correct two-widget shape was rejected: %v", err)
	}
}

// {{$}} into a widget renders the whole pipeline state as a form.
func TestRejectsWholeMessageIntoWidget(t *testing.T) {
	nodes := map[string]v1alpha1.TinyNode{
		"f.mod.debug-1": widgetNode("f.mod.debug-1", "", map[string]string{
			"in": `{"context":"{{$}}"}`,
		}),
	}
	err := checkWidgetShape(nodes)
	if err == nil {
		t.Fatal("whole-message passthrough into a widget was accepted")
	}
	if !strings.Contains(err.Error(), "{{$}}") {
		t.Errorf("error should name the offending mapping, got: %v", err)
	}
}

// The same passthrough is correct on a non-widget hop and must not be flagged.
func TestAllowsWholeMessageOnPlumbing(t *testing.T) {
	plumbing := widgetNode("f.mod.js-1", "", map[string]string{"request": `{"context":"{{$}}"}`})
	delete(plumbing.Labels, v1alpha1.DashboardLabel)
	if err := checkWidgetShape(map[string]v1alpha1.TinyNode{"f.mod.js-1": plumbing}); err != nil {
		t.Errorf("passthrough on plumbing was rejected: %v", err)
	}
}

// A widget mapping a specific field is what we want.
func TestAcceptsNamedAnswerField(t *testing.T) {
	nodes := map[string]v1alpha1.TinyNode{
		"f.mod.debug-1": widgetNode("f.mod.debug-1", "", map[string]string{
			"in": `{"answer":"{{$.outputData.messages[0].content}}"}`,
		}),
	}
	if err := checkWidgetShape(nodes); err != nil {
		t.Errorf("named answer field was rejected: %v", err)
	}
}
