package config

import (
	"errors"
	"fmt"
	"time"
)

// Defaults of the runner's spool (see internal/runner/spool.go).
const (
	DefaultSpoolMiB = 256
	DefaultSpoolAge = 15 * time.Minute
)

// SpoolConfig bounds what a runner keeps on its own disk while the server
// cannot be reached (see internal/runner/spool.go). Zero means the default.
type SpoolConfig struct {
	MaxMiB int    `yaml:"max_mib"` // MiB of log lines kept per step; the oldest are dropped, with a marker; default 256
	MaxAge string `yaml:"max_age"` // how old a spooled log line may be; default 15m
}

// Check reports a setting that cannot be used.
func (s SpoolConfig) Check() error {
	if s.MaxMiB < 0 || s.MaxMiB > 1<<20 {
		return errors.New("spool.max_mib must be between 0 and 1048576")
	}
	if _, err := s.age(); err != nil {
		return err
	}
	return nil
}

func (s SpoolConfig) age() (time.Duration, error) {
	if s.MaxAge == "" {
		return DefaultSpoolAge, nil
	}
	d, err := time.ParseDuration(s.MaxAge)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("spool.max_age: %q is not a duration such as 15m", s.MaxAge)
	}
	return d, nil
}

// Bytes is the size bound in bytes.
func (s SpoolConfig) Bytes() int64 {
	if s.MaxMiB <= 0 {
		return DefaultSpoolMiB << 20
	}
	return int64(s.MaxMiB) << 20
}

// Age is the age bound; the default if the setting is unusable (Check says so).
func (s SpoolConfig) Age() time.Duration {
	d, err := s.age()
	if err != nil {
		return DefaultSpoolAge
	}
	return d
}

// CheckLimits checks the limits a runner is configured with.
func (s RunnerSettings) CheckLimits() error {
	_, err := s.Reports.Limits()
	if err != nil {
		err = fmt.Errorf("runner.%w", err)
	}
	return errors.Join(err, s.Spool.Check())
}
