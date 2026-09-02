/*
Package sessions is the client side of the session runtime: list what runs,
see who needs a human, answer, start, attach. The TUI and the CLI commands
are thin skins over this.
*/
package sessions

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/actions"
	"github.com/tiny-systems/tiny/internal/addons"
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/workload"
)

// Version is the CLI's build stamp, shown on the fleet screen so a stale
// binary identifies itself.
// (set by the CLI; empty in tests)

// Target names the cluster this store talks to, for screens and prompts.
func (s *Store) Target() string {
	if s.Kube.ContextName == "" {
		return s.Kube.Namespace
	}
	return s.Kube.ContextName + "/" + s.Kube.Namespace
}

// Row is one line of the fleet screen: a session joined with its open
// question, if any.
type Row struct {
	Name     string
	Parent   string // spawning session, when this one was born via session_create
	Depth    int    // 0 for roots; children indent under their parent
	Phase    string
	Title    string // the agent's declared intent (status.title)
	Activity string // the agent's latest turn tail (status.activity) — always current
	CPU      string // self-reported cgroup sample
	Mem      string
	Message  string // status.message — why a session is stuck, when it is
	Task     string
	Pod      string
	Age      time.Duration
	Question *agentsv1.Question // open (unanswered) question, newest wins
}

// NeedsHuman reports whether the row should wear the amber mark.
func (r Row) NeedsHuman() bool { return r.Question != nil }

// Glyph is the row's one-character state.
func (r Row) Glyph() string {
	switch {
	case r.NeedsHuman():
		return "✳"
	case r.Phase == string(agentsv1.SessionRunning):
		return "●"
	case r.Phase == string(agentsv1.SessionDone):
		return "✓"
	case r.Phase == string(agentsv1.SessionFailed):
		return "✗"
	default:
		return "◌"
	}
}

// Store reads and writes the runtime's objects.
type Store struct {
	Kube    *kube.Client
	Version string
}

// Snapshot is one consistent read of the fleet.
type Snapshot struct {
	Rows []Row
	// Unattributed questions — no session to sit under, still someone waiting.
	Loose []agentsv1.Question
}

// Load lists sessions and questions and joins them.
func (s *Store) Load(ctx context.Context) (*Snapshot, error) {
	sessions := &agentsv1.SessionList{}
	if err := s.Kube.Client.List(ctx, sessions, client.InNamespace(s.Kube.Namespace)); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	questions := &agentsv1.QuestionList{}
	if err := s.Kube.Client.List(ctx, questions, client.InNamespace(s.Kube.Namespace)); err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	// No manager writes phases anymore: the fleet computes live truth from
	// the pods themselves.
	pods := &corev1.PodList{}
	if err := s.Kube.Client.List(ctx, pods, client.InNamespace(s.Kube.Namespace),
		client.MatchingLabels{appLabelKey: "tiny-session"}); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	live := map[string]*corev1.Pod{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if name := p.Labels[workload.SessionLabel]; name != "" {
			// Prefer a running pod over a terminating corpse.
			if cur, ok := live[name]; !ok || cur.DeletionTimestamp != nil {
				live[name] = p
			}
		}
	}
	return join(sessions.Items, questions.Items, live, time.Now()), nil
}

// livePhase reads a session's coarse state off its pod: the agent
// container is the session.
func livePhase(p *corev1.Pod) (phase, pod, message string) {
	if p == nil {
		return "starting", "", ""
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != creatingReason && w.Reason != initingReason {
			msg := w.Reason
			if w.Message != "" {
				msg += ": " + w.Message
			}
			return "stuck", p.Name, msg
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == agentContainer {
			switch {
			case cs.State.Running != nil:
				return "Running", p.Name, ""
			case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
				return "Failed", p.Name, strings.TrimSpace(cs.State.Terminated.Message)
			}
		}
	}
	if p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
				return "pending", p.Name, c.Message
			}
		}
	}
	return strings.ToLower(string(p.Status.Phase)), p.Name, ""
}

// join builds the screen's truth from the two lists. Exposed for tests.
func join(sessions []agentsv1.Session, questions []agentsv1.Question, live map[string]*corev1.Pod, now time.Time) *Snapshot {
	open := map[string]*agentsv1.Question{}
	var loose []agentsv1.Question
	for i := range questions {
		q := questions[i]
		if q.Answered() {
			continue
		}
		name := q.Spec.Session.Name
		if name == "" {
			loose = append(loose, q)
			continue
		}
		prev, ok := open[name]
		if !ok || q.CreationTimestamp.After(prev.CreationTimestamp.Time) {
			open[name] = &questions[i]
		}
	}

	snap := &Snapshot{Loose: loose}
	for i := range sessions {
		se := sessions[i]
		phase, podName, message := livePhase(live[se.Name])
		// A session whose workload is gone keeps whatever phase its status
		// recorded (Done, Failed) instead of looking forever-starting.
		if live[se.Name] == nil && se.Status.Phase != "" {
			phase = string(se.Status.Phase)
		}
		snap.Rows = append(snap.Rows, Row{
			Name:     se.Name,
			Parent:   se.Labels["tinysystems.io/parent"],
			Phase:    phase,
			Title:    se.Status.Title,
			Activity: se.Status.Activity,
			Message:  message,
			Task:     se.Spec.Task,
			CPU: func() string {
				if se.Status.Usage != nil {
					return se.Status.Usage.CPU
				}
				return ""
			}(),
			Mem: func() string {
				if se.Status.Usage != nil {
					return se.Status.Usage.Memory
				}
				return ""
			}(),
			Pod:      podName,
			Age:      now.Sub(se.CreationTimestamp.Time),
			Question: open[se.Name],
		})
	}
	// Creation order, oldest first: rows never jump when a question arrives
	// or ages shift — the ✳ glyph is the attention signal, position is not.
	slices.SortStableFunc(snap.Rows, func(a, b Row) int {
		switch {
		case a.Age > b.Age:
			return -1
		case a.Age < b.Age:
			return 1
		}
		return 0
	})
	snap.Rows = treeOrder(snap.Rows)
	return snap
}

// treeOrder keeps the needs-first sort for roots but tucks each child under
// its parent, one level deep — the spawn tree reads as a tree. A child whose
// parent is gone is shown as a root.
func treeOrder(rows []Row) []Row {
	children := map[string][]Row{}
	byName := map[string]bool{}
	for _, r := range rows {
		byName[r.Name] = true
	}
	var roots []Row
	for _, r := range rows {
		if r.Parent != "" && byName[r.Parent] {
			children[r.Parent] = append(children[r.Parent], r)
			continue
		}
		roots = append(roots, r)
	}
	out := make([]Row, 0, len(rows))
	for _, r := range roots {
		out = append(out, r)
		for _, c := range children[r.Name] {
			c.Depth = 1
			out = append(out, c)
		}
	}
	return out
}

// Answer resolves a question — and when the question carries an action and
// the answer allows it, THIS client performs the act with the answerer's
// own credentials: approval and execution are one gesture, audited as the
// human who made it. The empty answer is refused so a slip of the enter
// key cannot resolve someone's gate with nothing.
func (s *Store) Answer(ctx context.Context, questionName, answer string) error {
	if answer == "" {
		return fmt.Errorf("an empty answer resolves nothing")
	}
	q := &agentsv1.Question{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: questionName}, q); err != nil {
		return err
	}
	now := metav1.Now()
	q.Status.Answer = answer
	q.Status.AnsweredAt = &now
	q.Status.AnsweredBy = "tiny"
	if q.Spec.Action != nil && !q.Status.ActionDone {
		if agentsv1.AllowsAction(answer) {
			result, err := actions.Execute(ctx, s.Kube.Client, q)
			if err != nil {
				// The agent must hear the failure, not wait forever.
				result = "action failed: " + err.Error()
			}
			q.Status.Result = result
		} else {
			q.Status.Result = "denied"
		}
		q.Status.ActionDone = true
	}
	if err := s.Kube.Client.Status().Update(ctx, q); err != nil {
		return err
	}
	// The blocked tool call that raised this card may be long dead (pod
	// replaced, MCP client gave up) — an answer delivered only through it
	// can vanish silently. The inbox is the durable second copy: the agent
	// reads it in its prompt even when nothing was left listening.
	if session := q.Spec.Session.Name; session != "" {
		msg := fmt.Sprintf("Answer to your question %q (%s): %s",
			q.Name, firstLine(q.Spec.Text), answer)
		if q.Status.Result != "" && q.Status.Result != answer {
			msg += "\nResult: " + q.Status.Result
		}
		_ = s.SendText(ctx, session, msg) // best effort; the card itself is resolved
	}
	return nil
}

// firstLine trims a question to something an inbox line can carry.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i > 0 {
		text = text[:i]
	}
	if len(text) > 80 {
		text = text[:80] + "…"
	}
	return text
}

// Create starts a session.
// CreateOpts is everything a new session can be born with.
type CreateOpts struct {
	Name   string
	Task   string
	Repo   string
	Image  string
	Agent  string // claude (default) or codex
	Model  string // agent-specific model override
	CPU    string
	Memory string
	User   int64
	// EnvSecret names a secret (labeled for this session) whose keys land
	// in the agent's env — the Actions job token path.
	EnvSecret string
}

func (s *Store) Create(ctx context.Context, o CreateOpts) (*agentsv1.Session, error) {
	se := &agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace},
		Spec:       agentsv1.SessionSpec{Task: o.Task, Repo: o.Repo, Image: o.Image, Agent: o.Agent, Model: o.Model},
	}
	if o.CPU != "" || o.Memory != "" {
		se.Spec.Resources = &agentsv1.SessionResources{CPU: o.CPU, Memory: o.Memory}
	}
	if o.User > 0 {
		se.Spec.User = &o.User
	}
	if o.Name != "" {
		se.Name = o.Name
	} else {
		se.GenerateName = "s-"
	}
	if o.EnvSecret != "" {
		se.Annotations = map[string]string{"tinysystems.io/env-secret": o.EnvSecret}
	}
	if err := s.Kube.Client.Create(ctx, se); err != nil {
		return nil, err
	}
	// No manager exists: the creator materialises the workload, and the
	// ownerRef ties its lifetime to the Session CR.
	if err := workload.Ensure(ctx, s.Kube.Client, se); err != nil {
		return nil, fmt.Errorf("session created but workload failed: %w", err)
	}
	return se, nil
}

// Delete removes a session; its pod and workspace cascade away.
func (s *Store) Delete(ctx context.Context, name string) error {
	return s.Kube.Client.Delete(ctx, &agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace, Name: name},
	})
}

// Birth reports where a starting session is on its way to running, as a
// human-readable stage. Empty stage means the agent is up; failure carries a
// terminal reason (bad image, unschedulable) so callers stop waiting instead
// of hoping.
func (s *Store) Birth(ctx context.Context, name string) (stage, pod, failure string, err error) {
	se := &agentsv1.Session{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, se); err != nil {
		return "", "", "", err
	}
	// No manager records pods anymore: find the workload's pod live.
	pods := &corev1.PodList{}
	if err := s.Kube.Client.List(ctx, pods, client.InNamespace(s.Kube.Namespace),
		client.MatchingLabels{workload.SessionLabel: name}); err != nil {
		return "", "", "", err
	}
	if len(pods.Items) == 0 {
		return "creating workspace and pod", "", "", nil
	}
	p := &pods.Items[0]
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == agentContainer && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			if cs.State.Terminated.ExitCode >= 128 {
				return "restarting after interruption", p.Name, "", nil
			}
			return "", p.Name, orDefault(strings.TrimSpace(cs.State.Terminated.Message), "agent exited"), nil
		}
	}
	switch p.Status.Phase {
	case corev1.PodRunning:
		return "", p.Name, "", nil
	case corev1.PodFailed:
		reason := p.Status.Message
		for _, cs := range p.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil && t.Message != "" {
				reason = strings.TrimSpace(t.Message)
			}
		}
		return "", p.Name, orDefault(reason, "pod failed"), nil
	}
	// Pending: name the specific wait, and surface pull errors as terminal —
	// they self-retry forever and the user should see the reason now.
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName":
				return "", p.Name, fmt.Sprintf("%s: %s", w.Reason, w.Message), nil
			case creatingReason:
				return "pulling images / creating containers", p.Name, "", nil
			case initingReason:
				return "preparing workspace", p.Name, "", nil
			}
		}
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue {
			return "waiting for a node: " + c.Message, p.Name, "", nil
		}
	}
	return "starting", p.Name, "", nil
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// ClearAttention resolves a session's open attention cards — the hook-driven
// "waiting for your input" kind. Attaching IS the acknowledgement: the human
// is looking now. Tool questions (ask_human and gated actions) are real
// blockers and are never touched here.
func (s *Store) ClearAttention(ctx context.Context, session string) {
	list := &agentsv1.QuestionList{}
	if err := s.Kube.Client.List(ctx, list, client.InNamespace(s.Kube.Namespace)); err != nil {
		return // best effort: worst case the card stays until answered
	}
	for i := range list.Items {
		q := &list.Items[i]
		if q.Spec.Session.Name != session || q.Answered() || q.Spec.Reason != "notification" {
			continue
		}
		q.Status.Answer = "seen"
		q.Status.AnsweredBy = "attach"
		_ = s.Kube.Client.Status().Update(ctx, q)
	}
}

// settingsCM is the switchboard ConfigMap's well-known name; trueWord its
// on-state.
const (
	settingsCM = "tiny-settings"
	trueWord   = "true"
)

// NamespaceSettings mirrors the tiny-settings switchboard for the UI.
type NamespaceSettings struct {
	Zot          bool
	ZotNodeTrust bool
	Minio        bool
	RunnerRepo   string
	RepoKey      bool // tiny-repo-keys present (read-only status)
	// Observed truth per add-on: "", "running", "starting", or a failure.
	ZotState   string
	MinioState string
}

// LoadSettings reads the switchboard plus adjacent status.
func (s *Store) LoadSettings(ctx context.Context) (NamespaceSettings, error) {
	out := NamespaceSettings{}
	cm := &corev1.ConfigMap{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: settingsCM}, cm)
	if err == nil {
		out.Zot = cm.Data["zot"] == trueWord
		out.ZotNodeTrust = cm.Data["zotNodeTrust"] == trueWord
		out.Minio = cm.Data["minio"] == trueWord
		out.RunnerRepo = cm.Data["runnerRepo"]
	} else if !apierrors.IsNotFound(err) {
		return out, err
	}
	sec := &corev1.Secret{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: "tiny-repo-keys"}, sec); err == nil {
		out.RepoKey = true
	}
	out.ZotState = s.addonState(ctx, "tiny-zot", out.Zot)
	out.MinioState = s.addonState(ctx, "tiny-minio", out.Minio)
	return out, nil
}

// SaveSettings writes the switchboard; the manager reacts.
func (s *Store) SaveSettings(ctx context.Context, ns NamespaceSettings) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace, Name: settingsCM},
		Data: map[string]string{
			"zot":          boolWord(ns.Zot),
			"zotNodeTrust": boolWord(ns.ZotNodeTrust),
			"minio":        boolWord(ns.Minio),
			"runnerRepo":   ns.RunnerRepo,
		},
	}
	existing := &corev1.ConfigMap{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: settingsCM}, existing)
	if apierrors.IsNotFound(err) {
		if err := s.Kube.Client.Create(ctx, cm); err != nil {
			return err
		}
		return s.applyAddons(ctx, ns)
	}
	if err != nil {
		return err
	}
	existing.Data = cm.Data
	if err := s.Kube.Client.Update(ctx, existing); err != nil {
		return err
	}
	return s.applyAddons(ctx, ns)
}

// applyAddons makes the cluster match the switchboard — done HERE, by the
// toggling client, because nothing else is running to do it.
func (s *Store) applyAddons(ctx context.Context, ns NamespaceSettings) error {
	ap := &addons.Applier{Client: s.Kube.Client}
	namespace := s.Kube.Namespace
	if ns.Minio {
		if err := ap.EnsureMinio(ctx, namespace); err != nil {
			return fmt.Errorf("minio: %w", err)
		}
	} else if err := ap.TeardownMinioAddon(ctx, namespace); err != nil {
		return fmt.Errorf("minio teardown: %w", err)
	}
	if ns.Zot {
		if err := ap.EnsureZot(ctx, namespace, ns.ZotNodeTrust); err != nil {
			return fmt.Errorf("zot: %w", err)
		}
	} else if err := ap.TeardownZotAddon(ctx, namespace); err != nil {
		return fmt.Errorf("zot teardown: %w", err)
	}
	if ns.RunnerRepo != "" {
		img := workload.ResolveImages(ctx, s.Kube.Client, namespace).Sidecar
		if err := ap.EnsureRunnerAddon(ctx, namespace, ns.RunnerRepo, img); err != nil {
			return fmt.Errorf("runner: %w", err)
		}
	} else if err := ap.TeardownRunnerAddon(ctx, namespace); err != nil {
		return fmt.Errorf("runner teardown: %w", err)
	}
	return nil
}

func boolWord(b bool) string {
	if b {
		return trueWord
	}
	return "false"
}

// Add-on observed states, and the container whose life is the session.
const (
	stateRunning   = "running"
	stateStarting  = "starting"
	agentContainer = "agent"
	appLabelKey    = "app"
	creatingReason = "ContainerCreating"
	initingReason  = "PodInitializing"
)

// AddonState is one add-on's observed truth: "" (off), "running",
// "starting", or a failure reason worth reading.
func (s *Store) addonState(ctx context.Context, name string, enabled bool) string {
	if !enabled {
		return ""
	}
	dep := &appsv1.Deployment{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, dep); err != nil {
		if apierrors.IsNotFound(err) {
			return stateStarting // reconciler has not materialised it yet
		}
		return "unknown: " + err.Error()
	}
	if dep.Status.ReadyReplicas > 0 {
		return stateRunning
	}
	// Not ready: dig the pod for the reason, sessions-style.
	pods := &corev1.PodList{}
	if err := s.Kube.Client.List(ctx, pods, client.InNamespace(s.Kube.Namespace), client.MatchingLabels{appLabelKey: name}); err == nil {
		for _, p := range pods.Items {
			all := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
			for _, cs := range all {
				if w := cs.State.Waiting; w != nil && w.Reason != "" && w.Reason != creatingReason && w.Reason != initingReason {
					if w.Message != "" {
						return w.Reason + ": " + w.Message
					}
					return w.Reason
				}
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status != corev1.ConditionTrue && c.Message != "" {
					return c.Message
				}
			}
		}
	}
	return stateStarting
}

// Broadcast appends the same message to every unfinished session's inbox —
// the fleet-wide megaphone behind the TUI's [b] and `tiny broadcast`. Done
// sessions are skipped; everything else gets the message durably, including
// paused and restarting pods. Returns who was reached; per-session failures
// don't stop the rest.
func (s *Store) Broadcast(ctx context.Context, text string) (delivered []string, err error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty message")
	}
	list := &agentsv1.SessionList{}
	if err := s.Kube.Client.List(ctx, list, client.InNamespace(s.Kube.Namespace)); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var firstErr error
	for i := range list.Items {
		se := &list.Items[i]
		if se.Status.Phase == agentsv1.SessionDone || se.DeletionTimestamp != nil {
			continue
		}
		if err := s.SendText(ctx, se.Name, text); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", se.Name, err)
			}
			continue
		}
		delivered = append(delivered, se.Name)
	}
	if len(delivered) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return delivered, firstErr
}

// SendText appends a message to the session's durable inbox; the session's
// own pod delivers it into the agent's prompt and replays anything
// undelivered after a restart. Nothing is written on sand.
func (s *Store) SendText(ctx context.Context, session, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty message")
	}
	// The sidecar updates status on its own clock; a read-modify-write
	// without retry silently drops messages on conflict.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		se := &agentsv1.Session{}
		if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: session}, se); err != nil {
			return err
		}
		se.Spec.Inbox = append(se.Spec.Inbox, agentsv1.InboxMessage{
			ID:   fmt.Sprintf("m-%d", time.Now().UnixNano()),
			Text: text,
		})
		// The mailbox is not a log: keep the tail, the pod prunes nothing.
		if len(se.Spec.Inbox) > 20 {
			se.Spec.Inbox = se.Spec.Inbox[len(se.Spec.Inbox)-20:]
		}
		return s.Kube.Client.Update(ctx, se)
	})
}

// UploadFile puts a local file into the session's workspace under
// /workspace/uploads/ and drops an inbox line so the agent knows it
// arrived. This is what a file dragged onto the fleet screen becomes.
// Finished sessions work too — an inspection pod carries the copy.
func (s *Store) UploadFile(ctx context.Context, session, localPath string, progress func(done, total int64)) (string, error) {
	return s.uploadFile(ctx, session, localPath, true, progress)
}

func (s *Store) uploadFile(ctx context.Context, session, localPath string, announce bool, progress func(done, total int64)) (string, error) {
	st, err := os.Stat(localPath)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory — drop a file", localPath)
	}

	se := &agentsv1.Session{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: session}, se); err != nil {
		return "", err
	}
	pod, container, cleanup := se.Status.Pod, agentContainer, func() {}
	if pod == "" || se.Status.Phase != agentsv1.SessionRunning {
		shellPod, err := s.EnsureShellPod(ctx, session)
		if err != nil {
			return "", err
		}
		pod, container = shellPod, "shell"
		cleanup = func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			_ = s.DeleteShellPod(cctx, session)
		}
	}
	defer cleanup()

	remote, err := s.streamUpload(ctx, pod, container, localPath, progress)
	if err != nil {
		return "", err
	}
	if announce {
		// The agent discovers the file the same way it hears everything else.
		_ = s.SendText(ctx, session, "The human uploaded a file for you: "+remote)
	}
	return remote, nil
}

// UploadFileQuiet is UploadFile without the inbox announcement — used by
// the attached-terminal drop, where the substituted paste IS the
// announcement.
func (s *Store) UploadFileQuiet(ctx context.Context, session, localPath string, progress func(done, total int64)) (string, error) {
	return s.uploadFile(ctx, session, localPath, false, progress)
}

// droppedPath recognizes a terminal file-drop: pastes arrive as the path,
// possibly single-quoted (iTerm) or with backslash-escaped spaces.
func droppedPath(paste string) (string, bool) {
	p := strings.TrimSpace(paste)
	p = strings.Trim(p, "'\"")
	p = strings.ReplaceAll(p, `\ `, " ")
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		return "", false
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// PublishSessionSecret creates <session>-env labeled for that session —
// the only shape of secret the controller will mount from the env-secret
// annotation.
func (s *Store) PublishSessionSecret(ctx context.Context, session string, data map[string]string) error {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.Kube.Namespace,
			Name:      session + "-env",
			Labels:    map[string]string{"tinysystems.io/for-session": session},
		},
		StringData: data,
	}
	err := s.Kube.Client.Create(ctx, sec)
	if apierrors.IsAlreadyExists(err) {
		existing := &corev1.Secret{}
		if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: sec.Name}, existing); err != nil {
			return err
		}
		existing.StringData = data
		existing.Labels = sec.Labels
		return s.Kube.Client.Update(ctx, existing)
	}
	return err
}

// ListOutbox names the pending bundles in a session pod's outbox.
func (s *Store) ListOutbox(ctx context.Context, pod string) ([]string, error) {
	var out strings.Builder
	err := s.execStream(ctx, pod, agentContainer,
		[]string{"sh", "-c", "ls /workspace/outbox/*.bundle 2>/dev/null || true"},
		nil, &out, io.Discard)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, filepath.Base(line))
		}
	}
	return names, nil
}

// FetchOutboxBundle streams one bundle out of the pod to a local path and
// moves the original into outbox/.done — export is exactly-once.
func (s *Store) FetchOutboxBundle(ctx context.Context, pod, name, localPath string) error {
	if !bundleNameOK.MatchString(name) {
		return fmt.Errorf("bundle name %q is not a valid outbox name", name)
	}
	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	if err := s.execStream(ctx, pod, agentContainer,
		[]string{"cat", "/workspace/outbox/" + name}, nil, f, io.Discard); err != nil {
		_ = f.Close()
		_ = os.Remove(localPath)
		return err
	}
	return f.Close()
}

// bundleNameOK is the strict shape of an outbox bundle name — anything
// else (paths, dashes-first, shell metacharacters) is refused before any
// name reaches an exec argv.
var bundleNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.bundle$`)

// AckOutboxBundle retires a bundle AFTER its branch reached GitHub — the
// courier acks only what it pushed, so a failed push leaves the bundle in
// place for the next run. No shell: argv only.
func (s *Store) AckOutboxBundle(ctx context.Context, pod, name string) error {
	if !bundleNameOK.MatchString(name) {
		return fmt.Errorf("bundle name %q is not a valid outbox name", name)
	}
	if err := s.execStream(ctx, pod, agentContainer,
		[]string{"mkdir", "-p", "/workspace/outbox/.done"}, nil, io.Discard, io.Discard); err != nil {
		return err
	}
	return s.execStream(ctx, pod, agentContainer,
		[]string{"mv", "/workspace/outbox/" + name, "/workspace/outbox/.done/"},
		nil, io.Discard, io.Discard)
}
