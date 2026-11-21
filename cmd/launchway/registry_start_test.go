package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// What an environment runs is never deleted by retention or by hand, and
// the doctor says what is true of the registry as configured.
func TestARegistryImageAnEnvironmentRunsIsProtected(t *testing.T) {
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var svc *registry.Service
	svc = registry.New(registry.Options{DB: enginedb.New(eng.Database()), Blobs: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Protected: func(ctx context.Context, repo, digest string) bool { return deployedImage(ctx, eng, svc, repo, digest) }})
	ctx := context.Background()
	push := func(tag, content string) string {
		cfg := []byte(`{"os":"linux","architecture":"amd64","rootfs":{"type":"layers","diff_ids":[]},"c":"` + content + `"}`)
		ref := who(t, svc, "cart/api", cfg, []byte(content), tag)
		return ref
	}
	d1 := push("1.0", "one")
	d2 := push("2.0", "two")
	if _, err := eng.Database().Exec(ctx, `INSERT INTO environment_state(environment, project, service, current_version) VALUES('prod', 'cart', 'api', '1.0')`); err != nil {
		t.Fatal(err)
	}
	if !deployedImage(ctx, eng, svc, "cart/api", d1) || deployedImage(ctx, eng, svc, "cart/api", d2) {
		t.Fatal("which image prod runs")
	}
	if _, err := svc.DeleteManifest(ctx, "cart/api", d1, registry.Who{Name: "t"}); err == nil {
		t.Fatal("the image prod runs was deleted by hand")
	}
	if _, err := svc.DeleteManifest(ctx, "cart/api", d2, registry.Who{Name: "t"}); err != nil {
		t.Fatalf("an image nothing runs could not be deleted: %v", err)
	}
}

func who(t *testing.T, svc *registry.Service, repo string, cfg, layer []byte, tag string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.PutBlobDirect(ctx, repo, registry.SHA256(cfg), bytesReader(cfg), "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutBlobDirect(ctx, repo, registry.SHA256(layer), bytesReader(layer), "t"); err != nil {
		t.Fatal(err)
	}
	man := []byte(`{"schemaVersion":2,"mediaType":"` + registry.MediaOCIManifest + `","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + registry.SHA256(cfg) + `","size":` + itoa(len(cfg)) +
		`},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"` + registry.SHA256(layer) + `","size":` + itoa(len(layer)) + `}]}`)
	d, err := svc.PutManifest(ctx, repo, tag, registry.MediaOCIManifest, man, registry.Who{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDoctorRegistryNamesWhatStopsIt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.Mode = config.AuthNone
	r := checkRegistry(cfg)
	if !r.Enabled || len(r.Problems) == 0 || r.BlobsEncrypted || r.EncryptionNote == "" {
		t.Fatalf("%+v", r)
	}
	off := false
	cfg.Registry.Enabled = &off
	if r := checkRegistry(cfg); r.Enabled || len(r.lines()) != 1 {
		t.Fatalf("%+v", r)
	}
	cfg.Registry.Enabled = nil
	cfg.Auth.Mode = config.AuthLocal
	cfg.Registry.PublicURL = "https://registry.example.com"
	if r := checkRegistry(cfg); len(r.Problems) != 0 {
		t.Fatalf("%+v", r)
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
func itoa(n int) string                  { return strconv.Itoa(n) }
