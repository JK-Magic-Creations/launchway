package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForgeHostingIsOffByDefaultAndChecked(t *testing.T) {
	c, err := Load("")
	if err != nil || c.Forge.Hosting {
		t.Fatalf("hosting must be off by default: %+v %v", c.Forge, err)
	}
	p := filepath.Join(t.TempDir(), "l.yaml")
	if err := os.WriteFile(p, []byte("forge:\n  hosting: true\n  maintain_every: 30m\n  max_per_owner: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil || !c.Forge.Hosting || c.Forge.MaintainInterval() != 30*time.Minute || c.Forge.Check() != nil {
		t.Fatalf("%+v %v", c.Forge, err)
	}
	for _, bad := range []ForgeConfig{{MaxPerOwner: -1}, {MaintainEvery: "soon"}, {MaintainEvery: "5s"}} {
		if bad.Check() == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}
