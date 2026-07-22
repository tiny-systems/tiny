package adapters

import (
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
)

// mod builds a TinyModule with the given status name (what the operator
// publishes) and CRD resource name.
func mod(statusName, resourceName string) *v1alpha1.TinyModule {
	m := &v1alpha1.TinyModule{}
	m.Name = resourceName
	m.Status.Name = statusName
	return m
}

func TestModuleNameMatches(t *testing.T) {
	// A decentralized install names modules bare; the platform's docs and
	// get_instructions examples use the slash-qualified form. Both must
	// resolve against either shape of installed module.
	bare := mod("http-module-v0", "http-module-v0")
	qualified := mod("tinysystems/http-module-v0", "tinysystems-http-module-v0")

	cases := []struct {
		name   string
		wanted string
		m      *v1alpha1.TinyModule
		want   bool
	}{
		{"bare wanted, bare installed", "http-module-v0", bare, true},
		{"qualified wanted, bare installed", "tinysystems/http-module-v0", bare, true},
		{"dashed-qualified wanted, bare installed", "tinysystems-http-module-v0", bare, true},
		{"bare wanted, qualified installed", "http-module-v0", qualified, true},
		{"qualified wanted, qualified installed", "tinysystems/http-module-v0", qualified, true},
		{"case insensitive", "TinySystems/HTTP-Module-v0", bare, true},

		{"different module is not a match", "tinysystems/http-module-v0", mod("common-module-v0", "common-module-v0"), false},
		{"unrelated name", "llm-module-v0", bare, false},
		{"empty wanted", "", bare, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := moduleNameMatches(tc.wanted, tc.m); got != tc.want {
				t.Fatalf("moduleNameMatches(%q) = %v, want %v", tc.wanted, got, tc.want)
			}
		})
	}
}
