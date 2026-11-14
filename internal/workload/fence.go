package workload

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// fenceContainer waits, before the agent starts, until the egress fence
// holds the session pod. A NetworkPolicy reaches a new pod a moment after
// the pod starts (seconds on k3s), and an agent that starts first starts
// outside the fence.
const fenceContainer = "egress-fence"

// fenceWaitSeconds bounds the wait. A cluster whose CNI does not enforce
// NetworkPolicy never blocks the probe; its sessions start late rather
// than never (they were never fenced, and the settings screen says so).
const fenceWaitSeconds = 20

// fenceWait probes the node's kubelet port, which the fence blocks in
// both modes and a pod can otherwise reach. While a connection succeeds
// the fence is not live yet. A connection that fails, from the start or
// later, means the fence holds (or the port is closed anyway, and waiting
// would prove nothing more).
func fenceWait(image string, s *agentsv1.Session) corev1.Container {
	script := fmt.Sprintf(`for i in $(seq 1 %d); do
  timeout 1 bash -c 'exec 3<>/dev/tcp/$HOST_IP/10250' 2>/dev/null || exit 0
  sleep 1
done
echo "tiny: after %ds this pod still reaches its node past the egress fence; is NetworkPolicy enforced on this cluster?" >&2`,
		fenceWaitSeconds, fenceWaitSeconds)
	return corev1.Container{
		Name:            fenceContainer,
		Image:           image,
		ImagePullPolicy: PullPolicyFor(image),
		Command:         []string{"bash", "-c", script},
		Env: []corev1.EnvVar{{Name: "HOST_IP", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.hostIP"},
		}}},
		SecurityContext: confine(&corev1.SecurityContext{
			RunAsUser:    ptr(AgentUID),
			RunAsNonRoot: ptr(true),
		}, s),
	}
}
