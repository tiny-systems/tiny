package adapters

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
)

const crdSetupHint = `Tiny Systems CRDs are not installed in this cluster.

Set up with:

  helm repo add tinysystems https://tiny-systems.github.io/module/
  helm install tinysystems-crd tinysystems/tinysystems-crd \
    --namespace <NAMESPACE> --create-namespace

Then install at least one module. Use search_modules to find available
modules and get_module_info for the full helm install command.`

// wrapCRDError checks whether err indicates that the Tiny Systems CRDs
// are missing from the target cluster (the REST mapper can't resolve the
// GVK). If so it returns a user-friendly error with setup instructions
// instead of a raw Kubernetes discovery message. Otherwise it returns
// the original error unchanged.
//
// Apply this at the boundary of every adapter method that touches
// TinyModule / TinyNode / TinyFlow / TinyProject / TinyScenario CRDs
// so the LLM always gets an actionable message on a blank cluster.
func wrapCRDError(err error) error {
	if err == nil {
		return nil
	}
	if meta.IsNoMatchError(err) {
		return fmt.Errorf("%s", crdSetupHint)
	}
	return err
}
