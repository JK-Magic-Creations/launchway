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
