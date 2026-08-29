package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// SessionInfo is one row of session_list.
type SessionInfo struct {
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	Title      string `json:"title,omitempty"`
	Activity   string `json:"activity,omitempty"`
	Task       string `json:"task,omitempty"`
	Pod        string `json:"pod,omitempty"`
	Image      string `json:"image,omitempty"`
	CPURequest string `json:"cpuRequest,omitempty"`
	MemRequest string `json:"memoryRequest,omitempty"`
	CPUUsage   string `json:"cpuUsage,omitempty"`
	MemUsage   string `json:"memoryUsage,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ListInput has no parameters yet; reserved for filters.
type ListInput struct{}

// ListOutput carries the rows.
type ListOutput struct {
	Sessions []SessionInfo `json:"sessions"`
}

func (s *Server) sessionList(ctx context.Context, _ *mcp.CallToolRequest, _ ListInput) (*mcp.CallToolResult, ListOutput, error) {
	list := &agentsv1.SessionList{}
	if err := s.Client.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, ListOutput{}, fmt.Errorf("list sessions: %w", err)
	}
	out := ListOutput{Sessions: make([]SessionInfo, 0, len(list.Items))}
	for _, it := range list.Items {
		info := SessionInfo{
			Name:     it.Name,
			Phase:    string(it.Status.Phase),
			Title:    it.Status.Title,
			Activity: it.Status.Activity,
			Task:     it.Spec.Task,
			Pod:      it.Status.Pod,
			Image:    it.Spec.Image,
			Message:  it.Status.Message,
		}
		if r := it.Spec.Resources; r != nil {
			info.CPURequest, info.MemRequest = r.CPU, r.Memory
		}
		if u := it.Status.Usage; u != nil {
			info.CPUUsage, info.MemUsage = u.CPU, u.Memory
		}
		out.Sessions = append(out.Sessions, info)
	}
	return nil, out, nil
}

// CreateInput is what an agent supplies to start a sibling session.
type CreateInput struct {
	Name   string `json:"name,omitempty" jsonschema:"Name for the session. Generated when omitted."`
	Task   string `json:"task" jsonschema:"The task the new session starts with."`
	Repo   string `json:"repo,omitempty" jsonschema:"Repository to clone into the new session's workspace."`
	Image  string `json:"image,omitempty" jsonschema:"Container image the new session runs in — pick the toolchain the task needs (golang:1.26, maven:3-eclipse-temurin-21, ...). MUST be glibc-based with /bin/sh and git; alpine/musl images will not boot. Omit for the default agent image."`
	CPU    string `json:"cpu,omitempty" jsonschema:"CPU request for the session, kubernetes quantity (e.g. '2', '500m')."`
	Memory string `json:"memory,omitempty" jsonschema:"Memory request AND limit for the session (e.g. '4Gi')."`
	User   string `json:"user,omitempty" jsonschema:"Numeric uid to run as, for images wired to a specific non-root user — buildah's build user is 1000. Omit for the default."`
}

// CreateOutput names what was made.
type CreateOutput struct {
	Name   string `json:"name"`
	Result string `json:"result,omitempty"`
}

// sessionCreate parks a createSession action behind the gate. The sidecar
// holds no permission to make sessions — it writes the request; the
// controller, once a human allows it, is the one that acts.
func (s *Server) sessionCreate(ctx context.Context, _ *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, CreateOutput, error) {
	if strings.TrimSpace(in.Task) == "" {
		return nil, CreateOutput{}, fmt.Errorf("task is required")
	}
	asker := s.sessionFor(ctx)
	ask := fmt.Sprintf("Session %s wants to start a new session with the task: %q", orUnknown(asker.Name), in.Task)
	if in.Image != "" {
		ask += fmt.Sprintf(" in image %s", in.Image)
	}
	if in.CPU != "" || in.Memory != "" {
		ask += fmt.Sprintf(" (cpu %s, memory %s)", orDash(in.CPU), orDash(in.Memory))
	}
	ask += " — allow?"
	result, err := s.requestAction(ctx, asker, ask,
		agentsv1.QuestionAction{
			Type: agentsv1.ActionCreateSession,
			Params: map[string]string{
				"name":   in.Name,
				"task":   in.Task,
				"repo":   in.Repo,
				"image":  in.Image,
				"cpu":    in.CPU,
				"memory": in.Memory,
				"user":   in.User,
			},
		})
	if err != nil {
		return nil, CreateOutput{}, err
	}
	return nil, CreateOutput{Name: result, Result: result}, nil
}

// TitleInput is the agent's running self-description.
type TitleInput struct {
	Title string `json:"title" jsonschema:"A short present-tense description of what this session is doing right now, e.g. 'migrating auth tests to vitest'. Shown on the operator's fleet screen."`
}

// TitleOutput echoes what was set.
type TitleOutput struct {
	Title string `json:"title"`
}

// setTitle writes the agent's living title into its own Session status.
// Status is the one field of the world the voice may touch: it changes what
// screens SAY, never what the cluster DOES.
func (s *Server) setTitle(ctx context.Context, _ *mcp.CallToolRequest, in TitleInput) (*mcp.CallToolResult, TitleOutput, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, TitleOutput{}, fmt.Errorf("title is empty")
	}
	if len(title) > 120 {
		title = title[:120]
	}
	ref := s.sessionFor(ctx)
	if ref.Name == "" {
		return nil, TitleOutput{}, fmt.Errorf("cannot determine which session this is")
	}
	se := &agentsv1.Session{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: ref.Name}, se); err != nil {
		return nil, TitleOutput{}, fmt.Errorf("get session: %w", err)
	}
	se.Status.Title = title
	if err := s.Client.Status().Update(ctx, se); err != nil {
		return nil, TitleOutput{}, fmt.Errorf("update title: %w", err)
	}
	return nil, TitleOutput{Title: title}, nil
}

// StoreInput has no parameters: there is one store per namespace.
type StoreInput struct{}

// StoreOutput carries the alias command to run.
type StoreOutput struct {
	Result string `json:"result"`
}

// enableStore turns on the namespace artifact store through the gate. When
// the namespace policy pre-consents, the manager answers instantly; either
// way the result is the mc command that wires this session up.
func (s *Server) enableStore(ctx context.Context, _ *mcp.CallToolRequest, _ StoreInput) (*mcp.CallToolResult, StoreOutput, error) {
	asker := s.sessionFor(ctx)
	result, err := s.requestAction(ctx, asker,
		fmt.Sprintf("Session %s wants the namespace artifact store (minio) enabled — allow?", orUnknown(asker.Name)),
		agentsv1.QuestionAction{
			Type:   agentsv1.ActionEnableFeature,
			Params: map[string]string{"feature": "minio"},
		})
	if err != nil {
		return nil, StoreOutput{}, err
	}
	return nil, StoreOutput{Result: result}, nil
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func orUnknown(v string) string {
	if v == "" {
		return "(unattributed)"
	}
	return v
}
