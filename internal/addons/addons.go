/*
Package addons applies and removes the namespace's optional machinery —
the zot registry cache (TLS, node trust), the minio artifact store, the
GitHub Actions runner. No manager reconciles these anymore: the CLI
applies them the moment a settings toggle flips, and the gate's
enableFeature action applies them when an approved agent asks.
*/
package addons

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Applier wraps a client with the add-on verbs. Any credentialed caller —
// the CLI, a runner job — can be the applier; that is the point.
type Applier struct {
	client.Client
}

// MinioSecret is where the store's credentials live.
const MinioSecret = minioSecret

// EnsureMinio / TeardownMinio, EnsureZot / TeardownZot, EnsureRunner /
// TeardownRunner are the public verbs.
func (r *Applier) EnsureMinio(ctx context.Context, ns string) error { return r.ensureMinio(ctx, ns) }
func (r *Applier) TeardownMinioAddon(ctx context.Context, ns string) error {
	return r.teardownMinio(ctx, ns)
}

// EnsureZot applies the cache; nodeTrust adds/removes the DaemonSet.
func (r *Applier) EnsureZot(ctx context.Context, ns string, nodeTrust bool) error {
	ip, err := r.ensureService(ctx, ns)
	if err != nil {
		return err
	}
	if ip == "" {
		return fmt.Errorf("zot service has no ClusterIP yet — retry")
	}
	ca, err := r.ensureTLS(ctx, ns, ip)
	if err != nil {
		return err
	}
	if err := r.ensureZot(ctx, ns); err != nil {
		return err
	}
	if nodeTrust {
		return r.ensureTrustDS(ctx, ns, ip, ca)
	}
	return r.deleteIfExists(ctx, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: trustName}})
}

// TeardownZotAddon removes cache + trust (cache volume stays).
func (r *Applier) TeardownZotAddon(ctx context.Context, ns string) error { return r.teardown(ctx, ns) }

// EnsureRunnerAddon / TeardownRunnerAddon manage the Actions runner.
func (r *Applier) EnsureRunnerAddon(ctx context.Context, ns, repo, image string) error {
	return r.ensureRunner(ctx, ns, repo, image)
}
func (r *Applier) TeardownRunnerAddon(ctx context.Context, ns string) error {
	return r.teardownRunner(ctx, ns)
}

const (
	appLabel     = "app"
	caCrtKey     = "ca.crt"
	zotName      = "tiny-zot"
	zotPort      = 5000
	zotImage     = "ghcr.io/project-zot/zot:v2.1.0"
	trustName    = "tiny-zot-node-trust"
	zotTLSSecret = "tiny-zot-tls"
)

func (r *Applier) teardown(ctx context.Context, ns string) error {
	for _, obj := range []client.Object{
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: trustName}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotTLSSecret}},
		// The cache PVC stays: turning the feature off should not torch a
		// warm cache someone may re-enable tomorrow.
	} {
		if err := r.deleteIfExists(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func (r *Applier) deleteIfExists(ctx context.Context, obj client.Object) error {
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *Applier) ensureService(ctx context.Context, ns string) (string, error) {
	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: zotName}, svc)
	if apierrors.IsNotFound(err) {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName, Labels: map[string]string{appLabel: zotName}},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{appLabel: zotName},
				Ports:    []corev1.ServicePort{{Name: "registry", Port: zotPort}},
			},
		}
		if err := r.Create(ctx, svc); err != nil {
			return "", fmt.Errorf("create zot service: %w", err)
		}
	} else if err != nil {
		return "", err
	}
	return svc.Spec.ClusterIP, nil
}

// ensureTLS mints a CA and a serving cert for the Service IP, once. Returns
// the CA PEM for the node-trust DaemonSet.
func (r *Applier) ensureTLS(ctx context.Context, ns, ip string) ([]byte, error) {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: zotTLSSecret}, existing)
	if err == nil {
		return existing.Data[caCrtKey], nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tiny-zot CA " + ns},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	srvTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: zotName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP(ip)},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		return nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotTLSSecret},
		Data: map[string][]byte{
			caCrtKey:  caPEM,
			"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER}),
			"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER}),
		},
	}
	if err := r.Create(ctx, secret); err != nil {
		return nil, err
	}
	return caPEM, nil
}

// zotConfig is pull-through mode for Docker Hub: any repo, fetched on
// demand, kept on the cache volume.
const zotConfig = `{
  "storage": {"rootDirectory": "/var/lib/registry"},
  "http": {
    "address": "0.0.0.0", "port": "5000",
    "tls": {"cert": "/etc/zot/tls/tls.crt", "key": "/etc/zot/tls/tls.key"}
  },
  "extensions": {
    "sync": {
      "enable": true,
      "registries": [{
        "urls": ["https://docker.io"],
        "onDemand": true,
        "tlsVerify": true,
        "content": [{"prefix": "**"}]
      }]
    }
  },
  "log": {"level": "info"}
}`

func (r *Applier) ensureZot(ctx context.Context, ns string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName + "-config"},
		Data:       map[string]string{"config.json": zotConfig},
	}
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: cm.Name}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, cm); err != nil {
			return fmt.Errorf("create zot config: %w", err)
		}
	case err != nil:
		return err
	case existing.Data["config.json"] != zotConfig:
		existing.Data = cm.Data
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("update zot config: %w", err)
		}
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: zotName + "-cache"}, pvc); apierrors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName + "-cache"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
				},
			},
		}
		if err := r.Create(ctx, pvc); err != nil {
			return fmt.Errorf("create zot cache pvc: %w", err)
		}
	} else if err != nil {
		return err
	}

	dep := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Namespace: ns, Name: zotName}, dep)
	if apierrors.IsNotFound(err) {
		one := int32(1)
		dep = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: zotName, Labels: map[string]string{appLabel: zotName}},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: zotName}},
				// The cache volume is RWO: never two pods fighting over it.
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{appLabel: zotName}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "zot",
							Image: zotImage,
							Args:  []string{"serve", "/etc/zot/config.json"},
							Ports: []corev1.ContainerPort{{ContainerPort: zotPort}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config", MountPath: "/etc/zot/config.json", SubPath: "config.json"},
								{Name: "tls", MountPath: "/etc/zot/tls", ReadOnly: true},
								{Name: "cache", MountPath: "/var/lib/registry"},
							},
						}},
						Volumes: []corev1.Volume{
							{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: zotName + "-config"}}}},
							{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: zotTLSSecret}}},
							{Name: "cache", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: zotName + "-cache"}}},
						},
					},
				},
			},
		}
		if err := r.Create(ctx, dep); err != nil {
			return fmt.Errorf("create zot deployment: %w", err)
		}
		return nil
	}
	return err
}

// ensureTrustDS drops the cache's CA where BOTH container runtimes look —
// /etc/docker/certs.d and /etc/containerd/certs.d read per-pull, no daemon
// restart. Privileged only in the sense of hostPath; consented separately.
func (r *Applier) ensureTrustDS(ctx context.Context, ns, ip string, ca []byte) error {
	hostPort := fmt.Sprintf("%s:%d", ip, zotPort)
	script := fmt.Sprintf(`mkdir -p /host-docker/%[1]s /host-containerd/%[1]s
cp /ca/ca.crt /host-docker/%[1]s/ca.crt
cp /ca/ca.crt /host-containerd/%[1]s/ca.crt
printf 'server = "https://%[1]s"\n[host."https://%[1]s"]\n  capabilities = ["pull", "resolve"]\n  ca = "/etc/containerd/certs.d/%[1]s/ca.crt"\n' > /host-containerd/%[1]s/hosts.toml
echo trusted; sleep 2147483647`, hostPort)
	ds := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: trustName}, ds)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	dirType := corev1.HostPathDirectoryOrCreate
	ds = &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: trustName, Labels: map[string]string{appLabel: trustName}},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: trustName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{appLabel: trustName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "trust",
						Image:   "busybox:1.36",
						Command: []string{"sh", "-c", script},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "ca", MountPath: "/ca", ReadOnly: true},
							{Name: "docker-certs", MountPath: "/host-docker"},
							{Name: "containerd-certs", MountPath: "/host-containerd"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: zotTLSSecret, Items: []corev1.KeyToPath{{Key: caCrtKey, Path: caCrtKey}}}}},
						{Name: "docker-certs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/docker/certs.d", Type: &dirType}}},
						{Name: "containerd-certs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/etc/containerd/certs.d", Type: &dirType}}},
					},
				},
			},
		},
	}
	_ = ca // the CA travels via the secret volume; param kept for future rotation logic
	return r.Create(ctx, ds)
}

const (
	minioName    = "tiny-minio"
	minioImage   = "minio/minio:RELEASE.2024-12-18T13-15-44Z"
	minioPort    = 9000
	minioSecret  = "tiny-minio-creds"
	minioVolName = minioName + "-data"
)

func (r *Applier) ensureMinio(ctx context.Context, ns string) error {
	// Credentials first: the deployment consumes them.
	sec := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: minioSecret}, sec)
	if apierrors.IsNotFound(err) {
		buf := make([]byte, 20)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioSecret},
			StringData: map[string]string{
				"MINIO_ROOT_USER":     "tiny", //nolint:goconst // a name, not a shared symbol
				"MINIO_ROOT_PASSWORD": hex.EncodeToString(buf),
				// The endpoint rides along so consumers need one secret only.
				"TINY_STORE_ENDPOINT": "http://" + minioName + ":9000",
			},
		}
		if err := r.Create(ctx, sec); err != nil {
			return fmt.Errorf("create minio credentials: %w", err)
		}
	} else if err != nil {
		return err
	}

	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: minioVolName}, pvc); apierrors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioVolName},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
				},
			},
		}
		if err := r.Create(ctx, pvc); err != nil {
			return fmt.Errorf("create minio data pvc: %w", err)
		}
	} else if err != nil {
		return err
	}

	svc := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: minioName}, svc); apierrors.IsNotFound(err) {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioName, Labels: map[string]string{appLabel: minioName}},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{appLabel: minioName},
				Ports:    []corev1.ServicePort{{Name: "s3", Port: minioPort}},
			},
		}
		if err := r.Create(ctx, svc); err != nil {
			return fmt.Errorf("create minio service: %w", err)
		}
	} else if err != nil {
		return err
	}

	dep := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Namespace: ns, Name: minioName}, dep)
	if apierrors.IsNotFound(err) {
		one := int32(1)
		dep = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioName, Labels: map[string]string{appLabel: minioName}},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: minioName}},
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{appLabel: minioName}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "minio",
							Image: minioImage,
							Args:  []string{"server", "/data"},
							EnvFrom: []corev1.EnvFromSource{{
								SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: minioSecret}},
							}},
							Ports:        []corev1.ContainerPort{{ContainerPort: minioPort}},
							VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
						}},
						Volumes: []corev1.Volume{{
							Name:         "data",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: minioVolName}},
						}},
					},
				},
			},
		}
		if err := r.Create(ctx, dep); err != nil {
			return fmt.Errorf("create minio deployment: %w", err)
		}
		return nil
	}
	return err
}

func (r *Applier) teardownMinio(ctx context.Context, ns string) error {
	for _, obj := range []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioName}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: minioName}},
		// Credentials and DATA stay — artifacts outlive the toggle, like the
		// zot cache. Deleting the namespace is the real cleanup.
	} {
		if err := r.deleteIfExists(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

const (
	tinyBinVol   = "tiny-bin"
	tinyBinDir   = "/tiny-bin"
	verbGet      = "get"
	verbCreate   = "create"
	verbList     = "list"
	runnerName   = "tiny-runner"
	runnerImage  = "ghcr.io/actions/actions-runner:latest"
	runnerRegSec = "tiny-runner-reg"
)

// runnerScript registers ONCE with a one-hour registration token (pasted
// from GitHub's New-Runner page) and keeps its credentials on the state
// volume — no PAT, no long-lived secret anywhere. Restarts reuse the
// registration; the loop restarts the listener if it exits.
const runnerScript = `set -e
export PATH=/home/runner:$PATH
cp /tiny-bin/tiny /home/runner/tiny
mkdir -p /runner-state
# The runner writes .credentials/.runner beside its binaries; keep those
# on the volume by running out of a state dir seeded from the image.
if [ ! -f /runner-state/.runner ]; then
  cp -a /home/runner/* /home/runner/.[!.]* /runner-state/ 2>/dev/null || true
  cd /runner-state
  ./config.sh --url "https://github.com/$RUNNER_REPO" --token "$REG_TOKEN" \
    --name tiny-runner --labels tiny --unattended --replace
else
  cd /runner-state
fi
while :; do ./run.sh || true; echo "runner exited; restarting"; sleep 5; done`

func (r *Applier) ensureRunner(ctx context.Context, ns, repo, controllerImage string) error {
	if err := r.ensureRunnerRBAC(ctx, ns); err != nil {
		return err
	}
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: runnerName + "-state"}, pvc); apierrors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName + "-state"},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
				},
			},
		}
		if err := r.Create(ctx, pvc); err != nil {
			return fmt.Errorf("create runner state pvc: %w", err)
		}
	} else if err != nil {
		return err
	}
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: runnerName}, dep)
	if err == nil {
		// Repo change: recreate.
		if dep.Spec.Template.Spec.Containers[0].Env[0].Value != repo {
			if err := r.Delete(ctx, dep); err != nil {
				return err
			}
			return fmt.Errorf("runner repo changed; recreating")
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	one := int32(1)
	dep = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName, Labels: map[string]string{appLabel: runnerName}},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: runnerName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{appLabel: runnerName}},
				Spec: corev1.PodSpec{
					ServiceAccountName: runnerName,
					InitContainers: []corev1.Container{{
						Name:  "tiny-cli",
						Image: controllerImage,
						// Distroless image, no shell: the CLI installs itself.
						Command:      []string{"/tiny-cli", "--install-to", tinyBinDir},
						VolumeMounts: []corev1.VolumeMount{{Name: tinyBinVol, MountPath: tinyBinDir}},
					}},
					Containers: []corev1.Container{{
						Name:    "runner",
						Image:   runnerImage,
						Command: []string{"bash", "-c", runnerScript},
						Env: []corev1.EnvVar{
							{Name: "RUNNER_REPO", Value: repo},
							{Name: "REG_TOKEN", ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: runnerRegSec},
									Key:                  "REG_TOKEN",
									Optional:             ptr(true), // useless after first registration
								},
							}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: tinyBinVol, MountPath: tinyBinDir},
							{Name: "state", MountPath: "/runner-state"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name:         tinyBinVol,
							VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
						},
						{
							Name: "state",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: runnerName + "-state"},
							},
						},
					},
				},
			},
		},
	}
	return r.Create(ctx, dep)
}

// ensureRunnerRBAC: the runner's jobs create sessions and publish
// per-session env secrets — nothing else.
func (r *Applier) ensureRunnerRBAC(ctx context.Context, ns string) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName}}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"agents.tinysystems.io"}, Resources: []string{"sessions"}, Verbs: []string{verbGet, verbList, "watch", verbCreate, "update", "patch"}}, // update: deliveries append to spec.inbox
			// Manager-less: whoever creates a session creates its workload.
			{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{verbGet, verbCreate}},
			{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{verbGet, verbCreate}},
			{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{verbGet}},
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{verbCreate, "update", verbGet}},
			// The outbox courier lifts bundles out of session pods.
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{verbGet, verbList}},
			{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{verbCreate}},
			{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{verbGet, verbList}},
		},
	}
	if err := r.Create(ctx, role); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: runnerName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: runnerName, Namespace: ns}},
	}
	if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *Applier) teardownRunner(ctx context.Context, ns string) error {
	return r.deleteIfExists(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: runnerName}})
}

func ptr[T any](v T) *T { return &v }
