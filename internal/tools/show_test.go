package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	tinyv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func showServer(t *testing.T, funcs *interceptor.Funcs) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := tinyv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	se := &tinyv1.Session{ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "agents"}}
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(se).WithStatusSubresource(se)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	t.Setenv("TINY_SESSION_NAME", sessionName)
	return &Server{Client: b.Build(), Namespace: "agents"}
}

func shown(t *testing.T, s *Server) []tinyv1.ShownFile {
	t.Helper()
	se := &tinyv1.Session{}
	if err := s.Client.Get(context.Background(), client.ObjectKey{Namespace: "agents", Name: sessionName}, se); err != nil {
		t.Fatal(err)
	}
	return se.Status.Shown
}

func TestShowUserRecordsAndKeepsTheNewest(t *testing.T) {
	s := showServer(t, nil)
	ctx := context.Background()
	if _, out, err := s.showUser(ctx, nil, ShowInput{Path: "/workspace/./shots/../login.png", Caption: " the fix "}); err != nil || out.Shown != "/workspace/login.png" {
		t.Fatalf("showUser = %+v, %v", out, err)
	}
	got := shown(t, s)
	if len(got) != 1 || got[0].Path != "/workspace/login.png" || got[0].Caption != "the fix" || got[0].At.IsZero() {
		t.Fatalf("shown = %+v", got)
	}
	for i := range tinyv1.MaxShown + 5 {
		if _, _, err := s.showUser(ctx, nil, ShowInput{Path: fmt.Sprintf("/workspace/%d.png", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got = shown(t, s)
	if len(got) != tinyv1.MaxShown || got[len(got)-1].Path != fmt.Sprintf("/workspace/%d.png", tinyv1.MaxShown+4) {
		t.Fatalf("kept %d, last %q", len(got), got[len(got)-1].Path)
	}
}

func TestShowUserRefusesRelativePaths(t *testing.T) {
	s := showServer(t, nil)
	if _, _, err := s.showUser(context.Background(), nil, ShowInput{Path: "shot.png"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err = %v", err)
	}
}

// An old CRD drops status.shown silently; the agent must hear about it.
func TestShowUserSaysWhenTheRuntimeDropsIt(t *testing.T) {
	s := showServer(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			obj.(*tinyv1.Session).Status.Shown = nil
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	_, _, err := s.showUser(context.Background(), nil, ShowInput{Path: "/workspace/a.png"})
	if err == nil || !strings.Contains(err.Error(), "tiny init") {
		t.Fatalf("err = %v", err)
	}
}
