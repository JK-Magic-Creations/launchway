package hosts_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

func TestRegistryKinds(t *testing.T) {
	have := map[forge.Kind]bool{}
	for _, k := range hosts.Kinds() {
		have[k] = true
	}
	for _, k := range []forge.Kind{forge.KindGitHub, forge.KindGitLab, forge.KindGitea, forge.KindForgejo} {
		if !have[k] {
			t.Fatalf("kind %s is not registered: %v", k, hosts.Kinds())
		}
	}
	s, _ := setup(t)
	_, err := s.Add(t.Context(), hosts.AddInput{Name: "x", Kind: "nope", Credential: forge.SecretRef{Env: "X"}}, "d")
	if !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "github") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterIncompletePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	hosts.Register(hosts.Entry{Kind: forge.KindGitHub, New: nil})
}

// A kind that is named in the model but has no provider is not offered: a connection of it is refused, and
// the guide says "not yet" (plain is the example: it is the built-in no-host kind).
func TestKindsWithoutAProviderAreRefused(t *testing.T) {
	s, _ := setup(t)
	_, err := s.Add(t.Context(), hosts.AddInput{Name: "x", Kind: forge.KindPlain, Credential: forge.SecretRef{Env: "X"}}, "d")
	if !errors.Is(err, forge.ErrInvalid) || !strings.Contains(err.Error(), "is of kind") {
		t.Fatalf("%v", err)
	}
	for _, k := range hosts.Kinds() {
		if k == forge.KindPlain {
			t.Fatal("registered")
		}
	}
}
