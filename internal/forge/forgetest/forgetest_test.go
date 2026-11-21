package forgetest

import (
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// The suite passes a provider that declares everything none and answers every
// call as the declaration says, and refuses a kind that has no harness.
func TestSuiteOnAnAllNoneProvider(t *testing.T) {
	caps := forge.NoCapabilities("nothing here", "use a forge that has it")
	p := forge.NewFake(forge.KindPlain, caps)
	Run(t, Harness{Provider: p, Repo: forge.Repository{Path: "r"}})
	reg := forge.NewRegistry()
	if err := reg.Register(forge.KindPlain, forge.FakeFactory(forge.KindPlain, caps)); err != nil {
		t.Fatal(err)
	}
	RunRegistry(t, reg, map[forge.Kind]Harness{forge.KindPlain: {Provider: p, Repo: forge.Repository{Path: "r"}}})
}

// A provider that declares a feature native but leaves it to the base is not
// a lie the suite lets through: the call says unsupported, which Run reports.
// This runs the suite on a recording T to see that it fails.
func TestSuiteCatchesALyingDeclaration(t *testing.T) {
	caps := forge.NoCapabilities("nothing here", "use a forge that has it")
	caps[forge.CapRules] = forge.Declaration{Support: forge.Native}
	p := forge.NewFake(forge.KindGitea, caps)
	failed := false
	for _, feat := range []forge.Capability{forge.CapRules} {
		for _, call := range probes[feat] {
			err := call(t.Context(), p, forge.Repository{})
			if _, unsupported := forge.IsUnsupported(err); unsupported && p.Capabilities().Has(feat) {
				failed = true // exactly what Run would report as an error
			}
		}
	}
	if !failed {
		t.Fatal("a feature declared native but unimplemented was not noticed")
	}
}
