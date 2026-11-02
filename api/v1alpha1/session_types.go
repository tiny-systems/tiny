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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SessionSpec is what a session runs: one agent, one workspace, and
// optionally a task to start on.
type SessionSpec struct {
	// Task the agent starts with. Optional — a session without one boots
	// the agent idle, ready for a human to attach and talk.
	// +optional
	Task string `json:"task,omitempty"`
	// Repo to clone into the workspace before the agent starts. Optional —
	// a session can start on an empty workspace.
	// +optional
	Repo string `json:"repo,omitempty"`
	// Agent image to run. The image owns how the agent is started (tmux,
	// resume, hooks); the controller only wires task, workspace and sidecar.
	// +optional
	Image string `json:"image,omitempty"`
	// Agent selects which coding agent the payload starts: "claude"
	// (default) or "codex". Reaches the entrypoint as TINY_AGENT.
	// +kubebuilder:validation:Enum=claude;codex
	// +optional
	Agent string `json:"agent,omitempty"`
	// Model overrides the agent's default model (claude --model,
	// codex -m). Unset means the agent's own default.
	// +optional
	Model string `json:"model,omitempty"`
	// WorkspaceSize is the persistent workspace request. Defaults to 2Gi.
	// +optional
	WorkspaceSize string `json:"workspaceSize,omitempty"`
	// Resources sizes the agent container. Optional — unset means the
	// cluster's defaults.
	// +optional
	Resources *SessionResources `json:"resources,omitempty"`
	// User overrides the uid the agent runs as — for images whose tooling
	// is wired to a specific non-root user (buildah's build/1000). Never 0.
	// +kubebuilder:validation:Minimum=1
	// +optional
	User *int64 `json:"user,omitempty"`
	// Unconfined lifts the container hardening — seccomp RuntimeDefault,
	// every capability dropped, no privilege escalation — for images whose
	// tooling needs user namespaces or setuid helpers: rootless buildah and
	// podman. False is the hardened default, on purpose.
	// +optional
	Unconfined bool `json:"unconfined,omitempty"`
	// Inbox is the durable mailbox: messages for the agent, appended by
	// humans (the fleet screen's m key) and delivered into the agent's
	// prompt by its own pod — surviving restarts, usage-limit pauses, and
	// everything between. Delivered entries are pruned by the writer.
	// +optional
	Inbox []InboxMessage `json:"inbox,omitempty"`
	// MCPServers are extra MCP servers the agent registers next to tiny's
	// own. Reaches the entrypoint as TINY_MCP_CONFIG (claude) and
	// TINY_CODEX_MCP_ARGS (codex).
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	// +optional
	MCPServers []MCPServer `json:"mcpServers,omitempty"`
	// NodeSelector pins the session pod to matching nodes — a dedicated
	// node pool for browser or build sessions, say. Copied to the pod as is.
	// +kubebuilder:validation:MaxProperties=8
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations let the session pod onto tainted nodes; together with
	// NodeSelector they keep heavy sessions on their own pool and
	// everything else off it. Copied to the pod as is.
	// +kubebuilder:validation:MaxItems=8
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// MCPServer is an extra MCP server the agent registers next to tiny's own.
// Values are patterned to exclude whitespace, quotes and backslashes: they
// are rendered into env vars and codex -c overrides without escaping.
// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.command)",message="set exactly one of url or command"
// +kubebuilder:validation:XValidation:rule="self.name != 'tiny'",message="the name tiny is reserved for the built-in server"
// +kubebuilder:validation:XValidation:rule="!has(self.args) || has(self.command)",message="args requires command"
type MCPServer struct {
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,31}$`
	Name string `json:"name"`
	// HTTP endpoint. Mutually exclusive with Command.
	// +kubebuilder:validation:Pattern=`^https?://[^\s"'\\]+$`
	// +optional
	URL string `json:"url,omitempty"`
	// Stdio server started inside the agent container.
	// +kubebuilder:validation:Pattern=`^[^\s"'\\]+$`
	// +optional
	Command string `json:"command,omitempty"`
	// +kubebuilder:validation:items:Pattern=`^[^\s"'\\]+$`
	// +optional
	Args []string `json:"args,omitempty"`
}

// InboxMessage is one undelivered line for the agent.
type InboxMessage struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SessionResources is the deliberately small resource ask: one CPU figure,
// one memory figure. Requests carry both; memory is also the limit so a
// runaway build OOMs its own session instead of the node.
type SessionResources struct {
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +optional
	Memory string `json:"memory,omitempty"`
}

// SessionPhase is the coarse state a screen sorts by.
// +kubebuilder:validation:Enum=Pending;Running;Done;Failed
type SessionPhase string

// Session phases.
const (
	SessionRunning SessionPhase = "Running"
	SessionDone    SessionPhase = "Done"
	SessionFailed  SessionPhase = "Failed"
)

// SessionStatus is what the controller observed.
type SessionStatus struct {
	// Phase mirrors the workload's coarse state.
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`
	// Pod running the session, when one exists.
	// +optional
	Pod string `json:"pod,omitempty"`
	// Message says why, when a phase needs explaining.
	// +optional
	Message string `json:"message,omitempty"`
	// Title is the agent's own running description of what it is doing —
	// the living counterpart to spec.task, which never changes after birth.
	// Written by the session's sidecar via the set_title tool.
	// +optional
	Title string `json:"title,omitempty"`
	// Activity is the tail of the agent's most recent turn, refreshed by
	// the Stop hook on every turn end — the always-actual line a fleet
	// screen shows. Title is intent; Activity is now.
	// +optional
	Activity string `json:"activity,omitempty"`
	// Usage is the agent container's own accounting, self-reported from its
	// cgroup every ~30s — no metrics-server, no cluster-scoped RBAC.
	// +optional
	Usage *SessionUsage `json:"usage,omitempty"`
}

// SessionUsage is a point-in-time reading, human units.
type SessionUsage struct {
	// +optional
	CPU string `json:"cpu,omitempty"`
	// +optional
	Memory string `json:"memory,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.status.title`
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.spec.task`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Session is one coding-agent session running as a workload: a pod holding
// the agent and its MCP sidecar, and a persistent workspace that outlives the
// pod so the session resumes where it stopped.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SessionSpec   `json:"spec,omitempty"`
	Status SessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionList contains a list of Session.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Session{}, &SessionList{})
		return nil
	})
}
