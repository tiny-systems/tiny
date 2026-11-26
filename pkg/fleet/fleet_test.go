package fleet

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/workload"
)

func fakeClient(t *testing.T, ns string, objs ...client.Object) *Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := clientfake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &Client{Store: &sessions.Store{Kube: &kube.Client{Client: c, Namespace: ns}}}
}

func TestCopyAgentCredentials(t *testing.T) {
	from := fakeClient(t, "home", &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "home", Name: workload.AgentEnvSecret},
		Data:       map[string][]byte{"CLAUDE_CODE_OAUTH_TOKEN": []byte("tok")},
	})
	to := fakeClient(t, "new")
	if err := CopyAgentCredentials(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	got := &corev1.Secret{}
	key := client.ObjectKey{Namespace: "new", Name: workload.AgentEnvSecret}
	if err := to.Kube.Client.Get(t.Context(), key, got); err != nil {
		t.Fatal(err)
	}
	if string(got.Data["CLAUDE_CODE_OAUTH_TOKEN"]) != "tok" {
		t.Fatalf("copied %v", got.Data)
	}
	// Copying again replaces rather than fails.
	if err := CopyAgentCredentials(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	if err := CopyAgentCredentials(t.Context(), fakeClient(t, "empty"), to); err == nil {
		t.Fatal("copying from a fleet without credentials must fail")
	}
}
