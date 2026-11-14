package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestInitInstallsAFencedRuntime runs Init against a real API server with
// nothing on it, twice: the second run is an upgrade and must not fail.
func TestInitInstallsAFencedRuntime(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	ctx := context.Background()
	for i := range 2 {
		if err := Init(ctx, cfg, "bp", Options{}); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	for name, obj := range map[string]struct {
		key client.ObjectKey
		obj client.Object
	}{
		"namespace": {client.ObjectKey{Name: "bp"}, &corev1.Namespace{}},
		"sessions CRD": {
			client.ObjectKey{Name: "sessions.agents.tinysystems.io"}, &apiextensionsv1.CustomResourceDefinition{},
		},
		"service account": {client.ObjectKey{Namespace: "bp", Name: "tiny-session"}, &corev1.ServiceAccount{}},
		"egress policy":   {client.ObjectKey{Namespace: "bp", Name: "tiny-session-egress"}, &networkingv1.NetworkPolicy{}},
	} {
		if err := c.Get(ctx, obj.key, obj.obj); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The switchboard records the fence, so the next tiny init keeps it.
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "bp", Name: "tiny-settings"}, cm); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if cm.Data["egress"] != "true" {
		t.Errorf("settings egress = %q, want true", cm.Data["egress"])
	}
}

// TestInitRefusesAnUnfencedNamespace: a namespace whose switchboard has
// the fence off stays as configured, and Init says so instead of
// reporting a fenced install.
func TestInitRefusesAnUnfencedNamespace(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "open", Name: "tiny-settings"},
		Data:       map[string]string{"egress": "false"},
	}); err != nil {
		t.Fatal(err)
	}
	err = Init(ctx, cfg, "open", Options{})
	if err == nil || !strings.Contains(err.Error(), "egress") {
		t.Fatalf("Init on an unfenced namespace: %v, want an egress error", err)
	}
}

// TestInitTurnsTheEgressProxyOn: asked for the proxy, Init switches it on
// in the switchboard and deploys it.
func TestInitTurnsTheEgressProxyOn(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	ctx := context.Background()
	if err := Init(ctx, cfg, "bp", Options{EgressProxy: true, FenceWait: true}); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "bp", Name: "tiny-settings"}, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["egress"] != "true" || cm.Data["egressProxy"] != "true" || cm.Data["fenceWait"] != "true" {
		t.Fatalf("settings %v, want egress, egressProxy and fenceWait on", cm.Data)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "bp", Name: "tiny-egress"}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("proxy deployment: %v", err)
	}
}

func TestInitNeedsANamespace(t *testing.T) {
	if err := Init(context.Background(), nil, "", Options{}); err == nil {
		t.Fatal("Init with no namespace succeeded")
	}
}
