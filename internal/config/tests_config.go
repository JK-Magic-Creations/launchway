package config

import (
	"errors"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// TestsConfig limits what the server keeps of test reports. Zero means the
// default.
type TestsConfig struct {
	// MaxUploadMiB is how many MiB of report files the server accepts for
	// one step attempt; runners are told and send no more. Default 24.
	MaxUploadMiB int `yaml:"max_upload_mib"`
	// MaxCasesPerRun is how many individual test results one run keeps;
	// beyond it, only the totals. Default 50000.
	MaxCasesPerRun int `yaml:"max_cases_per_run"`
	// MaxCasesPerProject is how many a project keeps over all its runs: the
	// oldest runs' are removed first, keeping their totals. Default 1000000.
	MaxCasesPerProject int `yaml:"max_cases_per_project"`
	// MaxAgeDays is how long individual test results, and a test's
	// statistics, are kept: older results are removed whatever the count,
	// keeping each run's totals. Default 180; at most 3650.
	MaxAgeDays int `yaml:"max_age_days"`
}

// TestLimits converts the tests settings for the engine.
func (c *Config) TestLimits() (engine.TestLimits, error) {
	t := c.Tests
	if t.MaxUploadMiB < 0 || t.MaxCasesPerRun < 0 || t.MaxCasesPerProject < 0 || t.MaxAgeDays < 0 {
		return engine.TestLimits{}, errors.New("tests: limits cannot be negative")
	}
	if t.MaxAgeDays > 3650 {
		return engine.TestLimits{}, errors.New("tests: max_age_days is at most 3650")
	}
	if t.MaxUploadMiB > 1024 {
		return engine.TestLimits{}, errors.New("tests: max_upload_mib is at most 1024")
	}
	return engine.TestLimits{MaxUploadBytes: int64(t.MaxUploadMiB) << 20, MaxCasesPerRun: t.MaxCasesPerRun,
		MaxCasesPerProject: t.MaxCasesPerProject, MaxAgeDays: t.MaxAgeDays}, nil
}
