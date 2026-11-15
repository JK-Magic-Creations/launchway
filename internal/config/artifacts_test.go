package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "launchway.yaml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestArtifactAndFindingSettings(t *testing.T) {
	// What the Helm chart writes when everything is set.
	c, err := loadText(t, `
artifacts:
  store: s3
  retain: 14d
  max_retain: 8w
  max_file_mib: 2048
  max_run_mib: 8192
  max_project_gib: 100
  s3: {endpoint: "https://s3.example.com", bucket: b, region: eu-west-1, prefix: launchway/, path_style: false}
findings: {max_upload_mib: 8, max_per_run: 100, max_per_project: 1000, max_components_per_run: 10, max_components_per_project: 20, max_sbom_mib: 2}
`)
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.ArtifactLimits()
	if err != nil || o.DefaultRetain != 14*24*time.Hour || o.MaxRetain != 56*24*time.Hour || o.MaxFileBytes != 2048<<20 || o.MaxProjectBytes != 100<<30 {
		t.Fatalf("%+v %v", o, err)
	}
	if l, err := c.FindingLimits(); err != nil || l.MaxPerRun != 100 || l.MaxSBOMBytes != 2<<20 {
		t.Fatalf("%+v %v", l, err)
	}
	// Unset, the defaults: a directory under data.
	c, err = loadText(t, "data: d\n")
	if err != nil {
		t.Fatal(err)
	}
	if o, err := c.ArtifactLimits(); err != nil || o.MaxFileBytes != 0 {
		t.Fatalf("defaults %+v %v", o, err)
	}
	s, err := c.ArtifactStore()
	if err != nil || s.Name() != "filesystem" {
		t.Fatalf("store %v %v", s, err)
	}
	for text, want := range map[string]string{
		"artifacts: {store: tape}":                        "filesystem or s3",
		"artifacts: {store: s3}":                          "needs an s3: section",
		"artifacts: {s3: {endpoint: x}}":                  "store is not s3",
		"artifacts: {retain: forever}":                    "retain is a duration",
		"artifacts: {retain: 30d, max_retain: 7d}":        "max_retain at least retain",
		"artifacts: {max_file_mib: 100, max_run_mib: 10}": "must be at least max_file_mib",
		"artifacts: {max_file_mib: 900000}":               "max_file_mib is at most",
		"artifacts: {max_file_mib: -1}":                   "cannot be negative",
		"artifacts: {s3: {access_key: x}, store: s3}":     "field access_key not found",
		"findings: {max_per_run: 10, max_per_project: 5}": "max_per_project (5) must be at least max_per_run (10)",
		"findings: {max_upload_mib: 5000}":                "at most 1024",
	} {
		if _, err := loadText(t, text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", text, err, want)
		}
	}
}
