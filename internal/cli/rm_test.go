package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestYesNoDefaultsToNo(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, "\n": false, "n\n": false, "yep\n": false, "": false} {
		var out bytes.Buffer
		if got := yesNo(strings.NewReader(in), &out, "delete x?"); got != want {
			t.Errorf("answer %q: got %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Fatal("prompt must show that No is the default")
		}
	}
}

// Deleting every session takes the exact phrase, nothing looser.
func TestTypedConfirmNeedsTheExactPhrase(t *testing.T) {
	for in, want := range map[string]bool{"delete all\n": true, "  delete all  \n": true, "y\n": false, "Delete All\n": false, "delete\n": false} {
		if got := typedConfirm(strings.NewReader(in), &bytes.Buffer{}, typedDeleteAll); got != want {
			t.Errorf("typed %q: got %v, want %v", in, got, want)
		}
	}
}

func TestRmRefusesWithoutNamesOrWithBoth(t *testing.T) {
	for _, args := range [][]string{{}, {"--all", "s1"}} {
		cmd := newRmCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "not both, not neither") {
			t.Errorf("args %v: want the usage refusal, got %v", args, err)
		}
	}
}
