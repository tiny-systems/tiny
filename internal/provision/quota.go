package provision

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A ceiling on what a flow can create.
//
// Some components make real cluster objects: sandbox_run starts a Kubernetes
// Job per call, storage components claim volumes. A flow that loops — an agent
// talked into calling a tool repeatedly, a retry with no bound — makes as many
// as the loop runs, and nothing said no.
//
// Deliberately object counts only. A quota that also constrained cpu or memory
// would force every pod in the namespace to declare requests and limits, and
// creation of anything that did not would start failing — a runtime that stops
// installing modules is a worse outcome than the one being prevented. Object
// counts carry no such requirement.
//
// This is also not compute isolation, and it is worth being plain about that:
// module pods are shared across projects, so a hot flow saturates a pod rather
// than creating one. Capping the namespace's CPU would throttle every project
// equally, which is not isolation, just a smaller machine.

// QuotaName is the ResourceQuota this manages. Named for what it is so that
// somebody finding it in a namespace can tell it apart from their own.
const QuotaName = "tinysystems-guardrails"

// QuotaLimits is the ceiling. Zero means "do not constrain this".
type QuotaLimits struct {
	// Jobs bounds concurrent Kubernetes Jobs — sandbox_run's unit of work.
	// Completed jobs clear themselves after their TTL (300s), so this is a
	// ceiling on how many can be in flight at once, not on how many may ever
	// run.
	Jobs int64

	// PersistentVolumeClaims bounds volumes claimed by storage components.
	PersistentVolumeClaims int64
}

// DefaultQuota is generous enough that no honest flow meets it and tight enough
// that a runaway one stops. A legitimate flow rarely needs fifty sandbox runs
// in flight at once; a loop reaches fifty in seconds.
var DefaultQuota = QuotaLimits{Jobs: 50, PersistentVolumeClaims: 20}

// Empty reports whether this asks for nothing, in which case no quota is
// written at all — an object that constrains nothing is worse than no object,
// because it looks like protection.
func (q QuotaLimits) Empty() bool {
	return q.Jobs <= 0 && q.PersistentVolumeClaims <= 0
}

// EnsureQuota creates or updates the guardrail quota.
func EnsureQuota(ctx context.Context, cfg *rest.Config, namespace string, limits QuotaLimits) error {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	if limits.Empty() {
		// Asked for nothing: remove any ceiling this tool previously set rather
		// than leaving a stale one behind to surprise someone later.
		err := cs.CoreV1().ResourceQuotas(namespace).Delete(ctx, QuotaName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("remove quota: %w", err)
		}
		return nil
	}

	hard := hardFor(limits)

	want := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      QuotaName,
			Namespace: namespace,
			Labels:    map[string]string{managedLabel: "true"},
		},
		Spec: corev1.ResourceQuotaSpec{Hard: hard},
	}

	existing, err := cs.CoreV1().ResourceQuotas(namespace).Get(ctx, QuotaName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := cs.CoreV1().ResourceQuotas(namespace).Create(ctx, want, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create quota: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read quota: %w", err)
	}

	existing.Spec.Hard = hard
	existing.Labels = want.Labels
	if _, err := cs.CoreV1().ResourceQuotas(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update quota: %w", err)
	}
	return nil
}

// hardFor builds the ceiling. Object counts only — kept in one place so the
// rule is testable rather than a comment somebody trusts.
func hardFor(limits QuotaLimits) corev1.ResourceList {
	hard := corev1.ResourceList{}
	if limits.Jobs > 0 {
		hard["count/jobs.batch"] = *resource.NewQuantity(limits.Jobs, resource.DecimalSI)
	}
	if limits.PersistentVolumeClaims > 0 {
		hard["count/persistentvolumeclaims"] = *resource.NewQuantity(limits.PersistentVolumeClaims, resource.DecimalSI)
	}
	return hard
}

// QuotaUsage is what the cluster reports against the ceiling — the half that
// makes a quota worth having, since a limit nobody can see is one that only
// ever surfaces as a mysterious failure.
type QuotaUsage struct {
	Resource string
	Used     string
	Hard     string
}

// ReadQuota reports current usage, or nil when no quota is set.
func ReadQuota(ctx context.Context, cfg *rest.Config, namespace string) ([]QuotaUsage, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	q, err := cs.CoreV1().ResourceQuotas(namespace).Get(ctx, QuotaName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []QuotaUsage
	for name, hard := range q.Status.Hard {
		used := "0"
		if u, ok := q.Status.Used[name]; ok {
			used = u.String()
		}
		out = append(out, QuotaUsage{Resource: string(name), Used: used, Hard: hard.String()})
	}
	return out, nil
}
