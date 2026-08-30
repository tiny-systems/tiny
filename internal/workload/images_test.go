package workload

// The released CLI must pull the images built from its own tag; only a
// clean release version may move the default off "main" — dev stamps and
// dirty builds keep following main.

import "testing"

func TestDefaultImageTagFollowsReleaseVersionsOnly(t *testing.T) {
	t.Cleanup(func() { DefaultImageTag = "main" })
	cases := map[string]string{
		"v0.7.2":     "v0.7.2",
		"v10.20.30":  "v10.20.30",
		"dev":        "main",
		"dev-abc123": "main",
		"v0.7":       "main",
		"0.7.2":      "main",
		"v0.7.2-rc1": "main",
	}
	for version, want := range cases {
		DefaultImageTag = "main"
		SetDefaultImageTag(version)
		if DefaultImageTag != want {
			t.Errorf("SetDefaultImageTag(%q): tag = %q, want %q", version, DefaultImageTag, want)
		}
	}
	DefaultImageTag = "main"
	SetDefaultImageTag("v0.7.2")
	if got := DefaultAgentImage(); got != "ghcr.io/tiny-systems/agent:v0.7.2" {
		t.Errorf("DefaultAgentImage() = %q", got)
	}
}
