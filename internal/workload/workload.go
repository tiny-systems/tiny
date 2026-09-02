/*
Package workload builds what a Session runs as — WITHOUT a standing
manager. A session is a Deployment (replicas=1): Kubernetes' own
controllers resurrect its pod after kills, evictions and node deaths,
which is the resume story with zero tiny processes running. The PVC and
Deployment are ownerRef'd to the Session CR, so deleting a session sweeps
everything by plain garbage collection.

Whoever CREATES a session materialises its workload with their own
credentials: the CLI on tiny new, the runner job on tiny deliver
--ensure. An idle namespace runs nothing at all.
*/
package workload

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/settings"
)

const (
	defaultAgentRepo   = "ghcr.io/tiny-systems/agent"
	defaultSidecarRepo = "ghcr.io/tiny-systems/controller"

	// AgentEnvSecret is the by-convention credentials secret every agent
	// container loads (agent tokens, store creds). Written by tiny setup.
	AgentEnvSecret = "tiny-agent-env"
	// RepoKeysSecret holds the deploy key tiny setup mints.
	RepoKeysSecret = "tiny-repo-keys"
	// AgentUID is the fixed non-root uid agent and shell pods run as.
	AgentUID int64 = 61000

	tinyVolume      = "tiny"
	workspaceVolume = "workspace"
	workspaceMount  = "/workspace"
	agentContainer  = "agent"
	appLabel        = "app"
	// SessionLabel marks a session's pods and questions — the one label
	// every selector in the system uses.
	SessionLabel = "tinysystems.io/session"
)

// Images the workload runs; resolved from tiny-settings overrides
// (agentImage / sidecarImage keys) over the ghcr defaults.
type Images struct {
	Agent   string
	Sidecar string
}

// DefaultImageTag pins the ghcr default images to the CLI's own release.
// A released binary pulls the images built from the same tag; a dev build
// ("dev", commit stamps) follows main. Set once at startup from the CLI's
// version stamp.
var DefaultImageTag = "main"

// SetDefaultImageTag accepts the CLI's version and adopts it when it looks
// like a release tag (v1.2.3). Anything else keeps the default.
func SetDefaultImageTag(version string) {
	if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(version) {
		DefaultImageTag = version
	}
}

// DefaultAgentImage is the agent image a session gets when nothing
// overrides it.
func DefaultAgentImage() string { return defaultAgentRepo + ":" + DefaultImageTag }

// DefaultSidecarImage is the tiny-mcp sidecar's default image.
func DefaultSidecarImage() string { return defaultSidecarRepo + ":" + DefaultImageTag }

// ResolveImages reads per-namespace overrides — the dev loop's home.
func ResolveImages(ctx context.Context, c client.Client, ns string) Images {
	img := Images{Agent: DefaultAgentImage(), Sidecar: DefaultSidecarImage()}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: settings.Name}, cm); err == nil {
		if v := cm.Data["agentImage"]; v != "" {
			img.Agent = v
		}
		if v := cm.Data["sidecarImage"]; v != "" {
			img.Sidecar = v
		}
	}
	return img
}

func workspaceName(s *agentsv1.Session) string { return s.Name + "-workspace" }

// DeploymentName is the session's workload name.
func DeploymentName(s *agentsv1.Session) string { return s.Name + "-agent" }

func ptr[T any](v T) *T { return &v }

// Ensure materialises PVC + Deployment for the session, both owned by it.
func Ensure(ctx context.Context, c client.Client, s *agentsv1.Session) error {
	if err := ensureWorkspace(ctx, c, s); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	dep := &appsv1.Deployment{}
	err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: DeploymentName(s)}, dep)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	images := ResolveImages(ctx, c, s.Namespace)
	podSpec, labels, err := buildPodSpec(ctx, c, images, s)
	if err != nil {
		return err
	}
	one := int32(1)
	dep = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      DeploymentName(s),
			Namespace: s.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{SessionLabel: s.Name}},
			// The workspace is RWO: replacement must not overlap the corpse.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
	if err := controllerutil.SetControllerReference(s, dep, c.Scheme()); err != nil {
		return err
	}
	if err := c.Create(ctx, dep); err != nil {
		return fmt.Errorf("create deployment: %w", err)
	}
	return nil
}
func ensureWorkspace(ctx context.Context, c client.Client, s *agentsv1.Session) error {
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: workspaceName(s)}, pvc)
	if err == nil {
		// Found is not the same as ours. A claim mid-deletion, or one still
		// owned by an earlier session of the same name, is about to be swept
		// by GC — adopt it and the pod parks Pending on a claim that never
		// comes back. Fail loud instead; a retry moments later starts clean.
		if pvc.DeletionTimestamp != nil {
			return fmt.Errorf("workspace %s from an earlier session is still being deleted — retry in a moment or pick another name", workspaceName(s))
		}
		if owner := metav1.GetControllerOf(pvc); owner != nil && owner.UID != s.UID {
			return fmt.Errorf("workspace %s still belongs to an earlier session with this name — retry in a moment or pick another name", workspaceName(s))
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	size := s.Spec.WorkspaceSize
	if size == "" {
		size = "2Gi"
	}
	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("workspaceSize %q: %w", size, err)
	}
	pvc = &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: workspaceName(s), Namespace: s.Namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
			},
		},
	}
	if err := controllerutil.SetControllerReference(s, pvc, c.Scheme()); err != nil {
		return err
	}
	return c.Create(ctx, pvc)
}

//nolint:unparam // error reserved: settings/zot lookups may become fallible
func buildPodSpec(ctx context.Context, c client.Client, images Images, s *agentsv1.Session) (corev1.PodSpec, map[string]string, error) {
	agentImage := s.Spec.Image
	if agentImage == "" {
		agentImage = images.Agent
	}
	// The namespace cache, when on, gets between the USER's toolchain image
	// and Docker Hub. Only spec.image is rewritten: the operator's own
	// agent/sidecar/init images were chosen with their registries and pull
	// policies in mind (a local :dev image sent through the cache would ask
	// Docker Hub for a repository that exists on one laptop).
	initImage := "busybox:1.36"
	registryEnv := ""
	if cfg, err := settings.Load(ctx, c, s.Namespace); err == nil && cfg.Zot {
		if ip := zotIP(ctx, c, s.Namespace); ip != "" {
			// Agents learn the namespace registry's address from env — it
			// is a push target too (built images), not just a cache.
			registryEnv = fmt.Sprintf("%s:%d", ip, 5000)
			if s.Spec.Image != "" {
				agentImage = rewriteThroughCache(agentImage, ip)
			}
		}
	}

	workspace := corev1.VolumeMount{Name: workspaceVolume, MountPath: workspaceMount}
	tinyHome := corev1.VolumeMount{Name: tinyVolume, MountPath: "/tiny"}
	envSecret := verifiedEnvSecret(ctx, c, s)

	// The agent container's resources, when the session asks for them.
	var resources corev1.ResourceRequirements
	if res := s.Spec.Resources; res != nil {
		resources.Requests = corev1.ResourceList{}
		if res.CPU != "" {
			if q, err := resource.ParseQuantity(res.CPU); err == nil {
				resources.Requests[corev1.ResourceCPU] = q
			}
		}
		if res.Memory != "" {
			if q, err := resource.ParseQuantity(res.Memory); err == nil {
				resources.Requests[corev1.ResourceMemory] = q
				// Memory is also the limit: a runaway build OOMs its own
				// session, not the node.
				resources.Limits = corev1.ResourceList{corev1.ResourceMemory: q}
			}
		}
	}
	labels := map[string]string{
		SessionLabel: s.Name,
		appLabel:     "tiny-session",
	}
	podSpec := corev1.PodSpec{
		// The workspace survives; pods and containers are disposable. A
		// Deployment demands Always: an exited agent container restarts
		// in place and the entrypoint RESUMES from the workspace; dead
		// pods are replaced by Kubernetes itself — no tiny process
		// involved.
		RestartPolicy: corev1.RestartPolicyAlways,
		// The session's identity: may create Questions, nothing else.
		ServiceAccountName: "tiny-session",
		// The agent runs unprivileged (uid 61000); fsGroup makes the
		// freshly-provisioned workspace volume writable for it.
		SecurityContext: &corev1.PodSecurityContext{
			FSGroup: ptr(AgentUID),
		},
		// hostPath-backed provisioners (minikube, kind) ignore fsGroup, so
		// ownership is set explicitly, once, by a root init container.
		// fsGroup above still covers CSI volumes that do it properly.
		InitContainers: []corev1.Container{
			{
				Name:    "workspace-perms",
				Image:   initImage,
				Command: []string{"sh", "-c", fmt.Sprintf("chown %d:%d %s", AgentUID, AgentUID, workspaceMount)},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: ptr(int64(0)),
				},
				VolumeMounts: []corev1.VolumeMount{{Name: workspaceVolume, MountPath: workspaceMount}},
			},
			// The agent travels as a payload, not an image: claude, a
			// static tmux and the entrypoint are copied into /tiny, and
			// the session container — ANY glibc image — runs from there.
			// That is what lets spec.image be golang, maven, your
			// project's dev image, with nothing baked in.
			{
				Name:         "inject-agent",
				Image:        images.Agent,
				Command:      []string{"sh", "-c", "cp -a /opt/tiny/* /tiny/"}, // children, not the dir: the mountpoint's own metadata is not ours to set
				VolumeMounts: []corev1.VolumeMount{tinyHome},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: workspaceVolume,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: workspaceName(s)},
				},
			},
			{
				Name:         tinyVolume,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
			// Repo deploy key by convention (see below), and — when a
			// session has a published env secret — that secret as a
			// REFRESHING file volume: the token relay's delivery path.
			{
				Name: "repo-keys",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: RepoKeysSecret,
						Optional:   ptr(true),
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name:       agentContainer,
				Image:      agentImage,
				Command:    []string{"/tiny/entrypoint.sh"},
				WorkingDir: workspaceMount,
				Resources:  resources,
				// Explicit uid: a custom image may default to root, and
				// claude's bypass mode is not for root. spec.user
				// overrides for images wired to their own uid (buildah).
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:    ptr(agentUID(s)),
					RunAsNonRoot: ptr(true),
				},
				// Credentials by convention: the tiny-agent-env Secret
				// (ANTHROPIC_API_KEY and friends) lands in the agent's env,
				// plus — for spawner-born sessions — the trigger's own
				// secret, verified against the spawner. All optional: a
				// cluster without them still schedules, and the agent
				// reports a missing key as a question instead of
				// crashlooping.
				EnvFrom: sessionEnvFrom(envSecret),
				Env: []corev1.EnvVar{
					{Name: "TINY_TASK", Value: s.Spec.Task},
					{Name: "TINY_REPO", Value: s.Spec.Repo},
					{Name: "TINY_AGENT", Value: s.Spec.Agent},
					{Name: "TINY_MODEL", Value: s.Spec.Model},
					{Name: "TINY_SESSION_NAME", Value: s.Name},
					{Name: "TINY_REGISTRY", Value: registryEnv},
				},
				VolumeMounts: agentMounts(workspace, tinyHome, envSecret),
			},
			{
				Name:  "tiny-mcp",
				Image: images.Sidecar,
				Args:  []string{"serve", "--addr=127.0.0.1:8080"},
				Env: []corev1.EnvVar{
					{Name: "TINY_SESSION_NAME", Value: s.Name},
					{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
					}},
					{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
					}},
				},
			},
		},
	}
	if envSecret != "" {
		// The refreshing file external couriers deliver tokens through.
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "env-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: envSecret},
			},
		})
	}
	return podSpec, labels, nil
}

// agentMounts adds the refreshing token file when a session has a
// published env secret: env vars freeze at container start, secret VOLUMES
// keep syncing — the token relay only works through the file.
func agentMounts(workspace, tinyHome corev1.VolumeMount, envSecret string) []corev1.VolumeMount {
	m := []corev1.VolumeMount{workspace, tinyHome,
		{Name: "repo-keys", MountPath: "/tiny-keys", ReadOnly: true}}
	if envSecret != "" {
		m = append(m, corev1.VolumeMount{Name: "env-secret", MountPath: "/tiny-env", ReadOnly: true})
	}
	return m
}

// verifiedEnvSecret returns the annotation-named secret ONLY when that
// secret is itself labeled for this exact session — publishing a secret
// requires Secret-create rights, so the annotation can never smuggle a
// pre-existing namespace secret into a pod's env.
func verifiedEnvSecret(ctx context.Context, c client.Client, s *agentsv1.Session) string {
	extra := s.Annotations["tinysystems.io/env-secret"]
	if extra == "" {
		return ""
	}
	sec := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: extra}, sec); err != nil {
		return ""
	}
	if sec.Labels["tinysystems.io/for-session"] != s.Name {
		return ""
	}
	return extra
}

// sessionEnvFrom assembles the agent container's secret env: the agent
// token and store credentials by convention (both optional), and any
// secret a spawner pinned via annotation.
func sessionEnvFrom(extra string) []corev1.EnvFromSource {
	out := []corev1.EnvFromSource{
		{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: AgentEnvSecret},
			Optional:             ptr(true),
		}},
		{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "tiny-minio-creds"},
			Optional:             ptr(true),
		}},
	}
	if extra != "" {
		out = append(out, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: extra},
			Optional:             ptr(true),
		}})
	}
	return out
}

// agentUID is AgentUID unless the session claims an image-native uid.
func agentUID(s *agentsv1.Session) int64 {
	if s.Spec.User != nil && *s.Spec.User > 0 {
		return *s.Spec.User
	}
	return AgentUID
}

// zotIP finds the cache Service's ClusterIP — the address node runtimes can
// actually reach (they cannot resolve cluster DNS).
func zotIP(ctx context.Context, c client.Client, ns string) string {
	svc := &corev1.Service{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tiny-zot"}, svc); err != nil {
		return ""
	}
	return svc.Spec.ClusterIP
}

// rewriteThroughCache sends a bare-host image through the namespace cache:
// "golang:1.26" -> "<ip>:5000/library/golang:1.26". Images that already name
// a registry (ghcr.io/..., quay.io/...) pass untouched — the cache mirrors
// Docker Hub only, and Hub is where the rate limits live.
func rewriteThroughCache(image, ip string) string {
	if !strings.Contains(image, "/") {
		// Single-segment ("golang:1.26") cannot carry a registry host — the
		// colon is the TAG's. Hub canonical form is library/<name>.
		return fmt.Sprintf("%s:%d/library/%s", ip, 5000, image)
	}
	first, _, _ := strings.Cut(image, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return image // explicit registry: not ours to reroute
	}
	return fmt.Sprintf("%s:%d/%s", ip, 5000, image)
}
