package settings

import "testing"

func TestUnitNameProblem(t *testing.T) {
	for name, want := range map[string]bool{"payments": false, "Pay.ments_2-x": false, "": true, "two words": true, " lead": true, "-dash": true, "über": true} {
		if got := UnitNameProblem(name) != ""; got != want {
			t.Errorf("%q: problem=%v, want %v", name, got, want)
		}
	}
}

func TestSecretStoresNamed(t *testing.T) {
	text := "steps:\n  a:\n    secrets:\n      T: { store: vault, key: k }\n      U: { store: \"env\", key: u }\n  b:\n    secrets: { V: { store: vault, key: z } }\n"
	got := SecretStoresNamed(text)
	if len(got) != 2 || got[0] != "env" || got[1] != "vault" {
		t.Fatalf("%v", got)
	}
}
