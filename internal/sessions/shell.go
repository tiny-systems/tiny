package sessions

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/workload"
)

// EnsureShellPod makes sure an inspection pod for the session's workspace
// exists and is running, and returns its name. The pod is a plain shell on
// the same volume — for poking at what a session did (or left behind) with
// the agent's own toolset, without waking the agent. Callers exec into it;
// DeleteShellPod cleans up.
func (s *Store) EnsureShellPod(ctx context.Context, session string) (string, error) {
	return s.ensureShellPod(ctx, session, 0)
}

func (s *Store) ensureShellPod(ctx context.Context, session string, attempt int) (string, error) {
	se := &agentsv1.Session{}
	if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: session}, se); err != nil {
		return "", err
	}
	name := session + "-shell"
	pod := &corev1.Pod{}
	err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, pod)
	if apierrors.IsNotFound(err) {
		image := se.Spec.Image
		if image == "" {
			// Match the runtime's default so the shell has the agent's tools.
			image = workload.DefaultAgentImage()
		}
		pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: s.Kube.Namespace,
				Labels:    map[string]string{appLabelKey: "tiny-shell", "tinysystems.io/session": session},
				// The session owns the shell pod: deleting the session sweeps it.
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: agentsv1.GroupVersion.String(),
					Kind:       "Session",
					Name:       se.Name,
					UID:        se.UID,
				}},
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				SecurityContext: &corev1.PodSecurityContext{
					FSGroup: ptr(int64(61000)),
				},
				Volumes: []corev1.Volume{{
					Name: "workspace",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: session + "-workspace"},
					},
				}},
				Containers: []corev1.Container{{
					Name:       "shell",
					Image:      image,
					Command:    []string{"sh", "-c", "sleep 43200"}, // half a day, then it reaps itself
					WorkingDir: "/workspace",
					SecurityContext: &corev1.SecurityContext{
						RunAsUser:    ptr(int64(61000)),
						RunAsNonRoot: ptr(true),
					},
					VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
				}},
			},
		}
		if err := s.Kube.Client.Create(ctx, pod); err != nil {
			return "", fmt.Errorf("create shell pod: %w", err)
		}
	} else if err != nil {
		return "", err
	}

	// Wait until it can take an exec.
	for {
		if err := s.Kube.Client.Get(ctx, client.ObjectKey{Namespace: s.Kube.Namespace, Name: name}, pod); err == nil {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				return name, nil
			case corev1.PodFailed, corev1.PodSucceeded:
				// A leftover finished shell pod: replace it.
				if err := s.Kube.Client.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
					return "", err
				}
				// One replacement attempt; a second corpse means something
				// is actually wrong and recursing forever would hide it.
				if attempt >= 1 {
					return "", fmt.Errorf("shell pod %s keeps dying (phase %s) — check the image and the workspace PVC", name, pod.Status.Phase)
				}
				return s.ensureShellPod(ctx, session, attempt+1)
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for shell pod %s", name)
		case <-time.After(time.Second):
		}
	}
}

// DeleteShellPod removes the inspection pod, if any.
func (s *Store) DeleteShellPod(ctx context.Context, session string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: s.Kube.Namespace, Name: session + "-shell"}}
	err := s.Kube.Client.Delete(ctx, pod)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func ptr[T any](v T) *T { return &v }
