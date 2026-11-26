package fleet

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
)

func TestHealthReadsNodesAndSignIn(t *testing.T) {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	conds := []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "box"}, Status: corev1.NodeStatus{Conditions: conds}}
	env := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: workload.AgentEnvSecret},
		Data: map[string][]byte{"CLAUDE_CODE_OAUTH_TOKEN": []byte("x")}}
	gw := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: settings.Name},
		Data: map[string]string{settings.KeyModelGateway: settings.GatewayOAuth}}
	for name, tc := range map[string]struct {
		objs []runtimeObject
		want string
	}{
		"stored":  {[]runtimeObject{node, env}, SignInStored},
		"gateway": {[]runtimeObject{node, env, gw}, SignInGateway},
		"none":    {[]runtimeObject{node}, SignInNone},
	} {
		t.Run(name, func(t *testing.T) {
			b := clientfake.NewClientBuilder().WithScheme(s)
			for _, o := range tc.objs {
				b = b.WithObjects(o)
			}
			c := &Client{Store: &sessions.Store{Kube: &kube.Client{Client: b.Build(), Namespace: "agents",
				RESTConfig: &rest.Config{Host: "http://127.0.0.1:1"}}}}
			h, err := c.Health(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if h.SignIn != tc.want {
				t.Fatalf("sign-in %q, want %q", h.SignIn, tc.want)
			}
			if len(h.Nodes) != 1 || !h.Nodes[0].Ready || !h.Nodes[0].DiskPressure {
				t.Fatalf("nodes %+v", h.Nodes)
			}
			if h.Nodes[0].DiskFreeGB != -1 {
				t.Fatal("disk numbers must be -1 when the kubelet can't be read")
			}
		})
	}
}

type runtimeObject = interface {
	metav1.Object
	runtime.Object
}
