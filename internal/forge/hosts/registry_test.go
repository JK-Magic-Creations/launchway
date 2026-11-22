package hosts_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

func TestRegistryKinds(t *testing.T) {
	if k := hosts.Kinds(); len(k) == 0 || k[0] != forge.KindGitHub {
		t.Fatalf("kinds = %v", k)
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
