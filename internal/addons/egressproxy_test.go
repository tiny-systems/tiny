package addons

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A fresh install creates the proxy and re-applies it right away, while
// the deployment controller is writing the object's status. The update
// that loses that race must retry, not fail the install (seen on a new
// k3s cluster: "the object has been modified").
func TestProxyDeploymentUpdateRetriesOnConflict(t *testing.T) {
	conflicts := 1
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(testScheme(t)).Build(), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if conflicts > 0 {
				conflicts--
				return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, obj.GetName(), nil)
			}
			return cl.Update(ctx, obj, opts...)
		},
	})
	r := &Applier{Client: c}
	if err := r.ensureProxyDeployment(t.Context(), "ns", "img:1"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.ensureProxyDeployment(t.Context(), "ns", "img:2"); err != nil {
		t.Fatalf("an update that hit one conflict should retry and succeed: %v", err)
	}
	if conflicts != 0 {
		t.Fatal("the injected conflict was never hit")
	}
}
