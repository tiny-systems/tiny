/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SessionRef names the agent session a question belongs to, loosely: enough
// for a screen to group questions under the right row without this API
// depending on any particular runner's types.
type SessionRef struct {
	// Name of the session as its runner knows it (a kelos Session name, a
	// plain pod name — whatever the environment could determine).
	// +optional
	Name string `json:"name,omitempty"`
	// Kind of the referent, informational ("Session", "Pod").
	// +optional
	Kind string `json:"kind,omitempty"`
	// Pod that asked, when known. What attach targets.
	// +optional
	Pod string `json:"pod,omitempty"`
}

// QuestionSpec is what the agent asked.
type QuestionSpec struct {
	// Text is the question put to the human.
	// +kubebuilder:validation:MinLength=1
	Text string `json:"text"`
	// Options offered as one-press answers. Empty means free text.
	// +optional
	Options []string `json:"options,omitempty"`
	// Session the question came from, best effort.
	// +optional
	Session SessionRef `json:"session,omitempty"`
	// AskedBy is the tool caller's self-description, informational
	// ("claude-code", "codex").
	// +optional
	AskedBy string `json:"askedBy,omitempty"`
	// Reason distinguishes how the question arose: "tool" (the agent called
	// ask_human) or "notification" (the safety net saw the agent waiting).
	// +optional
	Reason string `json:"reason,omitempty"`
	// Action, when set, is what an "allow" answer authorizes. The sidecar
	// only ever writes this request; the controller is the one with the
	// permissions to carry it out. That split is the security model.
	// +optional
	Action *QuestionAction `json:"action,omitempty"`
}

// Action types the controller knows how to carry out.
const (
	ActionExposePort    = "exposePort"
	ActionCreateSession = "createSession"
	ActionEnableFeature = "enableFeature"
)

// Answer vocabulary for gated actions.
const (
	AnswerAllow = "allow"
	AnswerDeny  = "deny"
	AnswerYes   = "yes"
)

// AllowsAction reports whether an answer authorizes a gated action.
func AllowsAction(answer string) bool { return answer == AnswerAllow || answer == AnswerYes }

// QuestionAction is a requested act, parked until a human allows it.
type QuestionAction struct {
	// Type of act: exposePort or createSession.
	// +kubebuilder:validation:Enum=exposePort;createSession
	Type string `json:"type"`
	// Params of the act, by type:
	// exposePort: port, pod, name. createSession: task, repo, image.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// QuestionStatus is the human's side. Writing a non-empty answer resolves the
// question: the blocked ask_human call returns it to the agent.
type QuestionStatus struct {
	// Answer as the human gave it: one of the options, or free text.
	// +optional
	Answer string `json:"answer,omitempty"`
	// AnsweredAt is when the answer landed.
	// +optional
	AnsweredAt *metav1.Time `json:"answeredAt,omitempty"`
	// AnsweredBy is the answering client's self-description ("tiny tui",
	// "kubectl"), informational.
	// +optional
	AnsweredBy string `json:"answeredBy,omitempty"`
	// Result of the action after an allow: the controller reports what it
	// did (a URL, a session name) or the error it hit. The sidecar hands
	// this back to the waiting agent.
	// +optional
	Result string `json:"result,omitempty"`
	// ActionDone marks the action carried out (or terminally refused), so
	// the controller acts exactly once.
	// +optional
	ActionDone bool `json:"actionDone,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Session",type=string,JSONPath=`.spec.session.name`
// +kubebuilder:printcolumn:name="Question",type=string,JSONPath=`.spec.text`
// +kubebuilder:printcolumn:name="Answer",type=string,JSONPath=`.status.answer`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Question is one parked request for a human decision: an agent asked
// something it must not answer for itself, and the run waits. Everything
// else — who shows it, who answers it, how the agent resumes — is clients
// reading and patching this object.
type Question struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QuestionSpec   `json:"spec,omitempty"`
	Status QuestionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// QuestionList contains a list of Question.
type QuestionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Question `json:"items"`
}

// Answered reports whether a human has resolved the question.
func (q *Question) Answered() bool { return q.Status.Answer != "" }

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Question{}, &QuestionList{})
		return nil
	})
}
