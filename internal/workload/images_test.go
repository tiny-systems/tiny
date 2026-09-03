package workload

// The released CLI must pull the images built from its own tag; only a
// clean release version may move the default off "main" — dev stamps and
// dirty builds keep following main.

import "testing"

func TestDefaultImageTagFollowsReleaseVersionsOnly(t *testing.T) {
	t.Cleanup(func() { DefaultImageTag = "main" })
	cases := map[string]string{
		"0.7.2":      "0.7.2", // goreleaser's ldflags form
		"v0.7.2":     "0.7.2", // git-tag form, normalized
		"v10.20.30":  "10.20.30",
		"dev":        "main",
		"dev-abc123": "main",
		"v0.7":       "main",
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
	if got := DefaultAgentImage(); got != "ghcr.io/tiny-systems/agent:0.7.2" {
		t.Errorf("DefaultAgentImage() = %q", got)
	}
}
