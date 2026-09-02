/*
Package actions executes what an approved Question asks for — in the
ANSWERING client, with the answerer's own credentials. There is no
standing manager: pressing y on a ✳ card is both the approval and the
act, so the Kubernetes audit log names the human who did it. A question
answered by raw kubectl unblocks the asking agent (the sidecar returns
the answer text) but performs no act; acts belong to tiny clients.
*/
package actions

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/addons"
	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Execute performs an allowed question's action and returns its result
// string (which the sidecar hands to the blocked tool call).
func Execute(ctx context.Context, c client.Client, q *agentsv1.Question) (string, error) {
	p := q.Spec.Action.Params
	switch q.Spec.Action.Type {
	case agentsv1.ActionExposePort:
		session := p["session"]
		if session == "" {
			session = p["name"] // questions from before the session param
		}
		return exposePort(ctx, c, q.Namespace, p["pod"], p["name"], session, p["port"])
	case agentsv1.ActionCreateSession:
		return createSession(ctx, c, q, p)
	case agentsv1.ActionEnableFeature:
		return enableFeature(ctx, c, q.Namespace, p["feature"])
	default:
		return "", fmt.Errorf("unknown action type %q", q.Spec.Action.Type)
	}
}

func exposePort(ctx context.Context, c client.Client, namespace, pod, name, session, portStr string) (string, error) {
	port, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil || port <= 0 || port > 65535 {
		return "", fmt.Errorf("port %q is not a port", portStr)
	}
	if pod == "" {
		return "", fmt.Errorf("no pod to expose")
	}
	svcName := fmt.Sprintf("tiny-%s-%d", name, port)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "tiny"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{workload.SessionLabel: session},
			Ports:    []corev1.ServicePort{{Port: int32(port), TargetPort: intstr.FromInt32(int32(port))}},
		},
	}
	if se := sessionByName(ctx, c, namespace, session); se != nil {
		_ = controllerRef(c, se, svc) // best effort: ports die with their session
	}
	if err := c.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create service: %w", err)
	}
	return fmt.Sprintf("http://%s.%s.svc:%d", svcName, namespace, port), nil
}

func createSession(ctx context.Context, c client.Client, q *agentsv1.Question, p map[string]string) (string, error) {
	if p["task"] == "" {
		return "", fmt.Errorf("no task for the new session")
	}
	s := &agentsv1.Session{
		ObjectMeta: metav1.ObjectMeta{Namespace: q.Namespace},
		Spec: agentsv1.SessionSpec{
			Task:  p["task"],
			Repo:  p["repo"],
			Image: p["image"],
		},
	}
	if p["cpu"] != "" || p["memory"] != "" {
		s.Spec.Resources = &agentsv1.SessionResources{CPU: p["cpu"], Memory: p["memory"]}
	}
	if uid, err := strconv.ParseInt(p["user"], 10, 64); err == nil && uid > 0 {
		s.Spec.User = &uid
	}
	if name := p["name"]; name != "" {
		s.Name = name
	} else {
		s.GenerateName = "s-" // same prefix as tiny new — a session is a session
	}
	if q.Spec.Session.Name != "" {
		s.Labels = map[string]string{"tinysystems.io/parent": q.Spec.Session.Name}
	}
	if err := c.Create(ctx, s); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if err := workload.Ensure(ctx, c, s); err != nil {
		return "", fmt.Errorf("session created but workload failed: %w", err)
	}
	return s.Name, nil
}

func sessionByName(ctx context.Context, c client.Client, namespace, name string) *agentsv1.Session {
	if name == "" {
		return nil
	}
	se := &agentsv1.Session{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, se); err != nil {
		return nil
	}
	return se
}

// enableFeature flips the switchboard and, for minio, applies the add-on
// right here (no manager watches anymore) and waits briefly for the
// credentials so the blocked tool call gets a working alias.
func enableFeature(ctx context.Context, c client.Client, ns, feature string) (string, error) {
	if feature != "minio" {
		return "", fmt.Errorf("unknown feature %q", feature)
	}
	if err := setSettingsKey(ctx, c, ns, feature, "true"); err != nil {
		return "", err
	}
	if err := (&addons.Applier{Client: c}).EnsureMinio(ctx, ns); err != nil {
		return "", fmt.Errorf("apply minio: %w", err)
	}
	sec := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: addons.MinioSecret}, sec); err != nil {
		return "store enabling — credentials arrive in secret " + addons.MinioSecret + " shortly", nil //nolint:nilerr
	}
	return fmt.Sprintf(
		"store enabled — run: mc alias set store %s %s %s && mc mb --ignore-existing store/artifacts",
		string(sec.Data["TINY_STORE_ENDPOINT"]), string(sec.Data["MINIO_ROOT_USER"]), string(sec.Data["MINIO_ROOT_PASSWORD"])), nil
}

func setSettingsKey(ctx context.Context, c client.Client, ns, key, val string) error {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: settings.Name}, cm)
	if apierrors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: settings.Name},
			Data:       map[string]string{key: val},
		}
		return c.Create(ctx, cm)
	}
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = val
	return c.Update(ctx, cm)
}

// controllerRef sets owner if possible.
func controllerRef(c client.Client, owner *agentsv1.Session, obj metav1.Object) error {
	return controllerutil.SetControllerReference(owner, obj, c.Scheme())
}
