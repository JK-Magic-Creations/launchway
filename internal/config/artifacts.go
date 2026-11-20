package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ArtifactsConfig says where artifacts are kept and within which limits.
// Every setting is optional; zero means the built-in default.
type ArtifactsConfig struct {
	// Store is database, filesystem or s3. Unset, artifacts are kept in the
	// database on PostgreSQL (shared by every server, nothing else to run)
	// and in <data>/artifacts on SQLite; an installation that kept them on
	// the filesystem before the database store existed keeps doing so until
	// they are copied (launchway blobs copy). Several servers need one
	// store they all share: database, s3, or a directory on a shared
	// filesystem.
	Store string `yaml:"store"`
	// Database limits the database store. Optional; written by the Helm
	// chart only when set.
	Database *DatabaseStoreConfig `yaml:"database"`
	// Dir is the filesystem store's directory; default <data>/artifacts.
	Dir string `yaml:"dir"`
	// S3 configures the s3 store. Its credentials come from the
	// environment (AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or files
	// named by AWS_ACCESS_KEY_ID_FILE and AWS_SECRET_ACCESS_KEY_FILE), never
	// from this file.
	S3 *blobstore.S3Config `yaml:"s3"`
	// MaxFileMiB is the largest file an artifact may hold; default 1024.
	MaxFileMiB int `yaml:"max_file_mib"`
	// MaxRunMiB is how much one run may keep; default 4096.
	MaxRunMiB int `yaml:"max_run_mib"`
	// MaxProjectGiB is how much a project may keep over all its runs;
	// default 50.
	MaxProjectGiB int `yaml:"max_project_gib"`
	// Retain is how long artifacts are kept when the pipeline does not say
	// (30d, 2w, 12h); default 30d. MaxRetain is the longest any is kept,
	// whatever the pipeline says; default 90d.
	Retain    string `yaml:"retain"`
	MaxRetain string `yaml:"max_retain"`
}

// DatabaseStoreConfig limits the database store. Zero means the default.
type DatabaseStoreConfig struct {
	// MaxTotalGiB is how much the store may hold in all, counting uploads in
	// progress; default 100.
	MaxTotalGiB int `yaml:"max_total_gib"`
	// MaxBlobMiB is the largest single file; default 2048.
	MaxBlobMiB int `yaml:"max_blob_mib"`
}

// FindingsConfig limits what the server keeps of security findings and
// SBOMs. Zero means the default.
type FindingsConfig struct {
	MaxUploadMiB            int `yaml:"max_upload_mib"`             // SARIF per step attempt; default 32
	MaxPerRun               int `yaml:"max_per_run"`                // default 20000
	MaxPerProject           int `yaml:"max_per_project"`            // default 500000
	MaxComponentsPerRun     int `yaml:"max_components_per_run"`     // SBOM components; default 20000
	MaxComponentsPerProject int `yaml:"max_components_per_project"` // default 1000000
	MaxSBOMMiB              int `yaml:"max_sbom_mib"`               // SBOM read per attempt to summarise it; default 64
}

// ArtifactLimits converts the artifacts settings for the engine, without
// the store (see ArtifactStore).
func (c *Config) ArtifactLimits() (engine.ArtifactOptions, error) {
	a := c.Artifacts
	o := engine.ArtifactOptions{}
	if a.MaxFileMiB < 0 || a.MaxRunMiB < 0 || a.MaxProjectGiB < 0 {
		return o, errors.New("artifacts: limits cannot be negative")
	}
	// The largest file a store's 10,000 parts can hold.
	if most := int64(blobstore.MaxPartNumber) * engine.ArtifactChunkBytes >> 20; int64(a.MaxFileMiB) > most {
		return o, fmt.Errorf("artifacts: max_file_mib is at most %d", most)
	}
	if a.MaxProjectGiB > 1<<20 || a.MaxRunMiB > 1<<30 {
		return o, errors.New("artifacts: max_run_mib or max_project_gib is too large")
	}
	o.MaxFileBytes = int64(a.MaxFileMiB) << 20
	o.MaxRunBytes = int64(a.MaxRunMiB) << 20
	o.MaxProjectBytes = int64(a.MaxProjectGiB) << 30
	for _, r := range []struct {
		name string
		in   string
		out  *time.Duration
	}{{"retain", a.Retain, &o.DefaultRetain}, {"max_retain", a.MaxRetain, &o.MaxRetain}} {
		if r.in == "" {
			continue
		}
		d, ok := pipeline.ParseRetain(r.in)
		if !ok || d < pipeline.MinArtifactRetain || d > pipeline.MaxArtifactRetain {
			return o, fmt.Errorf("artifacts: %s is a duration from 1h to 3650d, such as 30d", r.name)
		}
		*r.out = d
	}
	if a.Database != nil {
		d := a.Database
		switch {
		case a.Store != "" && a.Store != "database":
			return o, errors.New("artifacts: database is set but store is not database (or unset)")
		case d.MaxTotalGiB < 0 || d.MaxBlobMiB < 0:
			return o, errors.New("artifacts: database limits cannot be negative")
		case d.MaxTotalGiB > 1<<20:
			return o, errors.New("artifacts: database.max_total_gib is too large")
		case d.MaxBlobMiB > 1<<20:
			return o, errors.New("artifacts: database.max_blob_mib is too large")
		}
		o.DatabaseLimits.MaxBytes = int64(d.MaxTotalGiB) << 30
		o.DatabaseLimits.MaxObjectBytes = int64(d.MaxBlobMiB) << 20
	}
	maxTotal, maxBlob := o.DatabaseLimits.MaxBytes, o.DatabaseLimits.MaxObjectBytes
	if maxTotal == 0 {
		maxTotal = blobstore.DefaultDatabaseMaxBytes
	}
	if maxBlob == 0 {
		maxBlob = blobstore.DefaultDatabaseMaxObjectBytes
	}
	switch a.Store {
	case "", "filesystem":
		if a.S3 != nil {
			return o, errors.New("artifacts: s3 is set but store is not s3")
		}
		o.Choice = engine.StoreAuto
		if a.Store == "filesystem" {
			o.Choice = engine.StoreGiven
		}
	case "database":
		if a.S3 != nil || a.Dir != "" {
			return o, errors.New("artifacts: s3 and dir are not for the database store")
		}
		o.Choice = engine.StoreDatabase
		if maxBlob > maxTotal {
			return o, fmt.Errorf("artifacts: database.max_blob_mib (%d) cannot be more than database.max_total_gib (%d GiB)", maxBlob>>20, maxTotal>>30)
		}
		if file := o.MaxFileBytes; file > maxBlob || (file == 0 && engine.DefaultArtifactFileBytes > maxBlob) {
			return o, fmt.Errorf("artifacts: max_file_mib (%d) cannot be more than database.max_blob_mib (%d), the largest file the database store keeps",
				max(file, engine.DefaultArtifactFileBytes)>>20, maxBlob>>20)
		}
	case "s3":
		if a.S3 == nil {
			return o, errors.New("artifacts: store s3 needs an s3: section (endpoint, bucket)")
		}
		if a.Dir != "" {
			return o, errors.New("artifacts: dir is for the filesystem store")
		}
		o.Choice = engine.StoreGiven
	default:
		return o, fmt.Errorf("artifacts: store is database, filesystem or s3, not %q", a.Store)
	}
	// Check the combination the engine checks, so a mistake shows when the
	// file is loaded.
	probe := o
	probe.StoreProblem = "-"
	if _, err := engine.CheckArtifactOptions(probe); err != nil {
		return o, err
	}
	return o, nil
}

// ArtifactStore makes the blob store the configuration describes: nil for
// the database store, which the engine makes from the database it opens.
// With no store configured it is the filesystem store, which the engine
// uses unless it picks the database (see engine.StoreAuto).
func (c *Config) ArtifactStore() (blobstore.Store, error) {
	switch c.Artifacts.Store {
	case "database":
		return nil, nil
	case "s3":
		creds, err := blobstore.CredentialsFromEnv()
		if err != nil {
			return nil, fmt.Errorf("artifacts: %w", err)
		}
		return blobstore.NewS3(*c.Artifacts.S3, creds)
	default:
		dir := c.Artifacts.Dir
		if dir == "" {
			dir = filepath.Join(c.Data, "artifacts")
		}
		return blobstore.NewFilesystem(dir)
	}
}

// FindingLimits converts the findings settings for the engine.
func (c *Config) FindingLimits() (engine.FindingLimits, error) {
	f := c.Findings
	if f.MaxUploadMiB < 0 || f.MaxPerRun < 0 || f.MaxPerProject < 0 || f.MaxComponentsPerRun < 0 || f.MaxComponentsPerProject < 0 || f.MaxSBOMMiB < 0 {
		return engine.FindingLimits{}, errors.New("findings: limits cannot be negative")
	}
	if f.MaxUploadMiB > 1024 || f.MaxSBOMMiB > 1024 {
		return engine.FindingLimits{}, errors.New("findings: max_upload_mib and max_sbom_mib are at most 1024")
	}
	l := engine.FindingLimits{MaxUploadBytes: int64(f.MaxUploadMiB) << 20, MaxPerRun: f.MaxPerRun, MaxPerProject: f.MaxPerProject,
		MaxComponentsPerRun: f.MaxComponentsPerRun, MaxComponentsPerProject: f.MaxComponentsPerProject, MaxSBOMBytes: int64(f.MaxSBOMMiB) << 20}
	if _, err := engine.CheckFindingLimits(l); err != nil {
		return l, err
	}
	return l, nil
}
