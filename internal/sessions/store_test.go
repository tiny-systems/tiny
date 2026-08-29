package sessions

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/kube"
)

func runningPod(session string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: session + "-agent"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "agent",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func session(name, phase string, age time.Duration, now time.Time) agentsv1.Session {
	return agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-age))},
		Spec:       agentsv1.SessionSpec{Task: "task of " + name},
		Status:     agentsv1.SessionStatus{Phase: agentsv1.SessionPhase(phase)},
	}
}

func question(name, sessionName, text, answer string, age time.Duration, now time.Time) agentsv1.Question {
	q := agentsv1.Question{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-age))},
		Spec:       agentsv1.QuestionSpec{Text: text, Session: agentsv1.SessionRef{Name: sessionName}},
	}
	q.Status.Answer = answer
	return q
}

// The screen's core truths: rows keep CREATION order (nothing jumps when a
// question arrives — the ✳ glyph is the signal, position is not), answered
// questions don't count, the newest open question represents its session.
func TestJoinKeepsCreationOrder(t *testing.T) {
	now := time.Now()
	snap := join(
		[]agentsv1.Session{
			session("api-fix", "Running", 1*time.Minute, now),
			session("flaky", "Running", 3*time.Hour, now),
			session("done", "Done", 5*time.Hour, now),
		},
		[]agentsv1.Question{
			question("q-old", "flaky", "old question", "", 2*time.Hour, now),
			question("q-new", "flaky", "newest question", "", 1*time.Minute, now),
			question("q-ans", "api-fix", "already answered", "yes", 1*time.Hour, now),
		},
		map[string]*corev1.Pod{
			"api-fix": runningPod("api-fix"),
			"flaky":   runningPod("flaky"),
			// "done" has no pod: a finished session's workload is gone.
		},
		now,
	)

	// Oldest first, question or not: done(5h), flaky(3h), api-fix(1m).
	for i, want := range []string{"done", "flaky", "api-fix"} {
		if snap.Rows[i].Name != want {
			t.Fatalf("row%d = %s, want %s — creation order must hold", i, snap.Rows[i].Name, want)
		}
	}
	flaky := snap.Rows[1]
	if !flaky.NeedsHuman() || flaky.Question.Name != "q-new" {
		t.Fatalf("newest open question must represent flaky: %+v", flaky.Question)
	}
	if snap.Rows[2].NeedsHuman() {
		t.Fatalf("an answered question must not mark a row: %+v", snap.Rows[2])
	}
	if g := flaky.Glyph(); g != "✳" {
		t.Fatalf("glyph %q", g)
	}
	if g := snap.Rows[0].Glyph(); g != "✓" {
		t.Fatalf("done glyph %q", g)
	}
}

// A question nobody can place still reaches the screen — someone is waiting.
func TestJoinKeepsUnattributedQuestions(t *testing.T) {
	now := time.Now()
	snap := join(nil, []agentsv1.Question{question("q-x", "", "hello?", "", time.Minute, now)}, nil, now)
	if len(snap.Loose) != 1 || snap.Loose[0].Spec.Text != "hello?" {
		t.Fatalf("loose = %+v", snap.Loose)
	}
}

// The megaphone reaches every unfinished session and only those: Done and
// deleting sessions are skipped, everyone else gets the same durable inbox
// line even when one delivery fails.
func TestBroadcastSkipsFinishedSessions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := agentsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	running := session("api-fix", "Running", time.Hour, now)
	pending := session("fresh", "", time.Minute, now)
	done := session("shipped", "Done", 2*time.Hour, now)
	for _, se := range []*agentsv1.Session{&running, &pending, &done} {
		se.Namespace = "default"
	}
	fc := clientfake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&running, &pending, &done).Build()
	store := &Store{Kube: &kube.Client{Client: fc, Namespace: "default"}}

	delivered, err := store.Broadcast(t.Context(), "demo at 10 — wrap up")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if len(delivered) != 2 {
		t.Fatalf("delivered = %v, want api-fix and fresh", delivered)
	}
	for _, name := range []string{"api-fix", "fresh"} {
		se := &agentsv1.Session{}
		if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: name}, se); err != nil {
			t.Fatal(err)
		}
		if len(se.Spec.Inbox) != 1 || se.Spec.Inbox[0].Text != "demo at 10 — wrap up" {
			t.Fatalf("%s inbox = %+v", name, se.Spec.Inbox)
		}
	}
	se := &agentsv1.Session{}
	if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "shipped"}, se); err != nil {
		t.Fatal(err)
	}
	if len(se.Spec.Inbox) != 0 {
		t.Fatalf("done session got broadcast: %+v", se.Spec.Inbox)
	}
}
