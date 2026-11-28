package sessions

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/addons"
)

func allowList(body string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: addons.ProxyAllowConfigMap},
		Data:       map[string]string{"hosts": body},
	}
}

func TestEgressStateModes(t *testing.T) {
	for _, tc := range []struct {
		data map[string]string
		want string
	}{
		{map[string]string{"egress": boolWord(false)}, EgressOpen},
		{map[string]string{"egress": trueWord}, EgressFenced},
		{map[string]string{"egress": trueWord, "egressProxy": trueWord}, EgressAllowList},
	} {
		s, _ := modelAuthStore(t, switchboard(tc.data), allowList("github.com\n# docs\n.npmjs.org\n"))
		got, err := s.EgressState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got.Mode != tc.want {
			t.Errorf("%v: mode %q, want %q", tc.data, got.Mode, tc.want)
		}
		if tc.want == EgressAllowList {
			if strings.Join(got.Hosts, ",") != "github.com,.npmjs.org" {
				t.Errorf("hosts %v", got.Hosts)
			}
		} else if got.Hosts != nil {
			t.Errorf("%s: hosts should be nil, got %v", tc.want, got.Hosts)
		}
	}
}

func TestAllowAndRemoveHost(t *testing.T) {
	s, c := modelAuthStore(t, switchboard(map[string]string{"egress": trueWord, "egressProxy": trueWord}),
		allowList("# tiny's defaults\ngithub.com # code\n.npmjs.org\n"))
	ctx := t.Context()
	if added, err := s.AllowHost(ctx, "  PyPI.org "); err != nil || !added {
		t.Fatalf("allow: %v %v", added, err)
	}
	if added, _ := s.AllowHost(ctx, "pypi.org"); added {
		t.Error("second allow should report already there")
	}
	for _, bad := range []string{"https://x.com", "x.com:443", "*.x.com", "a b", "x.com/path", ""} {
		if _, err := s.AllowHost(ctx, bad); err == nil {
			t.Errorf("allow %q should be refused", bad)
		}
	}
	if removed, err := s.RemoveHost(ctx, "github.com"); err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if removed, _ := s.RemoveHost(ctx, "github.com"); removed {
		t.Error("second remove should report not there")
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "agents", Name: addons.ProxyAllowConfigMap}, cm); err != nil {
		t.Fatal(err)
	}
	body := cm.Data["hosts"]
	if strings.Contains(body, "github.com") || !strings.Contains(body, "# tiny's defaults") || !strings.Contains(body, "pypi.org") {
		t.Errorf("list after edits:\n%s", body)
	}
}

func TestSetEgressModeKeepsTheGatewaysProxy(t *testing.T) {
	s, _ := modelAuthStore(t, switchboard(map[string]string{"egress": trueWord, "egressProxy": trueWord, "modelGateway": "oauth"}))
	err := s.SetEgressMode(t.Context(), EgressFenced)
	if err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("want a gateway refusal, got %v", err)
	}
	if err := s.SetEgressMode(t.Context(), "everything"); err == nil {
		t.Error("unknown mode should be refused")
	}
}

func TestSetEgressModeOpen(t *testing.T) {
	s, c := modelAuthStore(t, switchboard(map[string]string{"egress": trueWord, "agentImage": "keep-me"}))
	if err := s.SetEgressMode(t.Context(), EgressOpen); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: settingsCM}, cm); err != nil {
		t.Fatal(err)
	}
	if cm.Data["egress"] != boolWord(false) || cm.Data["agentImage"] != "keep-me" {
		t.Errorf("switchboard after open: %v", cm.Data)
	}
}

func TestParseRefusal(t *testing.T) {
	r, ok := parseRefusal("2026-11-28T10:00:01.5Z egress DENIED CONNECT Evil.example:443")
	if !ok || r.Host != "evil.example" || r.Lookup || r.Last.IsZero() {
		t.Errorf("connect: %+v %v", r, ok)
	}
	r, ok = parseRefusal("2026-11-28T10:00:02Z egress DENIED DNS lookup.example.")
	if !ok || r.Host != "lookup.example" || !r.Lookup || !r.Last.Equal(time.Date(2026, 11, 28, 10, 0, 2, 0, time.UTC)) {
		t.Errorf("dns: %+v %v", r, ok)
	}
	if _, ok := parseRefusal("2026-11-28T10:00:02Z egress allowed CONNECT x:443"); ok {
		t.Error("an allowed line is not a refusal")
	}
}
