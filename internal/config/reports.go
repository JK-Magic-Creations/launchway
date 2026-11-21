package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// ServerReportsConfig bounds test attachments, coverage and HTML reports (schema
// 18), and says where hosted HTML reports are served from. Every setting
// is optional; the defaults are built in.
type ServerReportsConfig struct {
	MaxAttachmentFiles         int `yaml:"max_attachment_files"`           // files in a step's attachments directory; default 2000
	MaxAttachmentMiB           int `yaml:"max_attachment_mib"`             // default 512
	MaxHTMLFiles               int `yaml:"max_html_files"`                 // files in one HTML report; default 5000
	MaxHTMLMiB                 int `yaml:"max_html_mib"`                   // default 256
	MaxCoverageUploadMiB       int `yaml:"max_coverage_upload_mib"`        // coverage reports per step attempt; default 24
	MaxCoverageFilesPerRun     int `yaml:"max_coverage_files_per_run"`     // default 20000
	MaxCoverageFilesPerProject int `yaml:"max_coverage_files_per_project"` // default 2000000
	// Origin is a separate origin (https://reports.example.com) that serves
	// hosted HTML reports, on its own host name pointing at the same
	// servers. Without it, they are served from Launchway's own origin,
	// sandboxed (see docs/guide/test-reports.md, "HTML reports").
	Origin string `yaml:"origin"`
	// BrandName and BrandLogo brand the exported Debriefs (HTML and PDF);
	// without them the exports carry the product's name and no logo.
	// BrandLogo is the path of a PNG or JPEG file of at most 256 KiB.
	BrandName string `yaml:"brand_name"`
	BrandLogo string `yaml:"brand_logo"`
	// ChangeCoverageFloor is the share of a pull request's changed lines
	// that should be covered by tests, 0 to 100 (default 80). It advises in
	// the Debrief and the pull request summary; nothing fails.
	ChangeCoverageFloor float64 `yaml:"change_coverage_floor"`
}

// DebriefBrand returns the name and the logo (as a data URL) for exported
// Debriefs, and the advised floor for a change's coverage.
func (c *Config) DebriefBrand() (name, logo string, floor float64, err error) {
	r := c.Reports
	name = strings.TrimSpace(r.BrandName)
	if len(name) > 60 || strings.ContainsAny(name, "\x00\r\n") {
		return "", "", 0, errors.New("reports: brand_name is at most 60 characters on one line")
	}
	if r.ChangeCoverageFloor < 0 || r.ChangeCoverageFloor > 100 {
		return "", "", 0, errors.New("reports: change_coverage_floor is from 0 to 100")
	}
	if r.BrandLogo != "" {
		data, err := os.ReadFile(r.BrandLogo)
		if err != nil {
			return "", "", 0, fmt.Errorf("reports: brand_logo: %w", err)
		}
		if len(data) > 256<<10 {
			return "", "", 0, errors.New("reports: brand_logo is larger than 256 KiB")
		}
		switch {
		case len(data) > 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
			logo = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
			logo = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
		default:
			return "", "", 0, errors.New("reports: brand_logo must be a PNG or JPEG file")
		}
	}
	return name, logo, r.ChangeCoverageFloor, nil
}

// ReportOptions converts the reports settings for the engine.
func (c *Config) ReportOptions() (engine.ReportOptions, error) {
	r := c.Reports
	if r.MaxAttachmentFiles < 0 || r.MaxAttachmentMiB < 0 || r.MaxHTMLFiles < 0 || r.MaxHTMLMiB < 0 || r.MaxCoverageUploadMiB < 0 ||
		r.MaxCoverageFilesPerRun < 0 || r.MaxCoverageFilesPerProject < 0 {
		return engine.ReportOptions{}, errors.New("reports: limits cannot be negative")
	}
	if r.MaxAttachmentMiB > 1<<20 || r.MaxHTMLMiB > 1<<20 || r.MaxCoverageUploadMiB > 1024 {
		return engine.ReportOptions{}, errors.New("reports: max_coverage_upload_mib is at most 1024, and the other sizes at most 1048576")
	}
	o := engine.ReportOptions{AttachmentFiles: r.MaxAttachmentFiles, AttachmentBytes: int64(r.MaxAttachmentMiB) << 20,
		HTMLFiles: r.MaxHTMLFiles, HTMLBytes: int64(r.MaxHTMLMiB) << 20, CoverageUploadBytes: int64(r.MaxCoverageUploadMiB) << 20,
		CoverageFilesPerRun: r.MaxCoverageFilesPerRun, CoverageFilesPerProject: r.MaxCoverageFilesPerProject}
	if _, err := engine.CheckReportOptions(o); err != nil {
		return o, err
	}
	return o, nil
}

// ReportsOrigin returns the separate origin for hosted HTML reports, without
// a trailing slash, or "".
func (c *Config) ReportsOrigin() (string, error) {
	o := strings.TrimSpace(c.Reports.Origin)
	if o == "" {
		return "", nil
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("reports: origin must be a scheme and host only, such as https://reports.example.com (got %q)", o)
	}
	return u.Scheme + "://" + u.Host, nil
}
