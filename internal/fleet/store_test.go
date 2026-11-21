package fleet

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestStoreKeepsAnAirfieldsFleet(t *testing.T) {
	e, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s, ctx := &Store{DB: e.Database()}, context.Background()
	hs := testHosts(3)
	hs[2].Group = "new"
	if err := s.SetHosts(ctx, "prod", hs); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHosts(ctx, "dev", testHosts(1)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Hosts(ctx, "prod")
	if err != nil || len(got) != 3 || got[2].Group != "new" || got[0].User != "deploy" || got[0].Member != "app/web-1" {
		t.Fatalf("%+v %v", got, err)
	}
	// Replacing, not adding.
	if err := s.SetHosts(ctx, "prod", hs[:1]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Hosts(ctx, "prod"); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	if got, _ := s.Hosts(ctx, "dev"); len(got) != 1 {
		t.Fatal("another Airfield's fleet changed")
	}
	// A host with no pinned key, or twice, is refused and changes nothing.
	bad := testHosts(2)
	bad[1].HostKey = ""
	if err := s.SetHosts(ctx, "prod", bad); err == nil || !strings.Contains(err.Error(), "host_key") {
		t.Fatalf("%v", err)
	}
	if err := s.SetHosts(ctx, "prod", append(testHosts(1), testHosts(1)...)); err == nil {
		t.Fatal("a duplicate was accepted")
	}
	if got, _ := s.Hosts(ctx, "prod"); len(got) != 1 {
		t.Fatalf("a refused change changed the fleet: %+v", got)
	}
}
