package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// ExposeInput asks for a port on the caller's own pod to be reachable.
// paramSession names the requesting session in gated-action params — the
// selector target, distinct from the human-chosen service name.
const paramSession = "session"

type ExposeInput struct {
	Port int32  `json:"port" jsonschema:"The port your process is listening on."`
	Name string `json:"name,omitempty" jsonschema:"Short name for the service. Defaults to your session name."`
}

// ExposeOutput says where the port now lives.
type ExposeOutput struct {
	URL string `json:"url"`
}

// exposePort parks an exposePort action behind the gate. The sidecar cannot
// create Services — it writes the request; the controller, once a human
// allows it, wires the Service to the caller's pod and reports the URL back
// through the Question.
func (s *Server) exposePort(ctx context.Context, _ *mcp.CallToolRequest, in ExposeInput) (*mcp.CallToolResult, ExposeOutput, error) {
	if in.Port <= 0 || in.Port > 65535 {
		return nil, ExposeOutput{}, fmt.Errorf("port %d is not a port", in.Port)
	}
	caller := s.sessionFor(ctx)
	if caller.Pod == "" {
		return nil, ExposeOutput{}, fmt.Errorf("cannot tell which pod is asking — expose_port needs caller attribution")
	}
	name := in.Name
	if name == "" {
		name = caller.Name
	}
	result, err := s.requestAction(ctx, caller,
		fmt.Sprintf("Session %s wants port %d of pod %s exposed in-cluster — allow?", orUnknown(caller.Name), in.Port, caller.Pod),
		agentsv1.QuestionAction{
			Type: agentsv1.ActionExposePort,
			Params: map[string]string{
				"port":       fmt.Sprintf("%d", in.Port),
				"pod":        caller.Pod,
				"name":       name,
				paramSession: caller.Name,
			},
		})
	if err != nil {
		return nil, ExposeOutput{}, err
	}
	return nil, ExposeOutput{URL: result}, nil
}

// requestAction parks a Question carrying an action and waits for the
// controller's result — the same gate ask_human uses, applied to the
// toolbox's own dangerous tools. A deny comes back as an error naming it.
func (s *Server) requestAction(ctx context.Context, who agentsv1.SessionRef, text string, action agentsv1.QuestionAction) (string, error) {
	q, err := s.createQuestion(ctx, agentsv1.QuestionSpec{
		Text:    text,
		Options: []string{agentsv1.AnswerAllow, agentsv1.AnswerDeny},
		Session: who,
		Reason:  ReasonTool,
		Action:  &action,
	})
	if err != nil {
		return "", err
	}
	res, err := s.waitForResult(ctx, q.Name)
	if err != nil {
		return "", err
	}
	return res, nil
}
