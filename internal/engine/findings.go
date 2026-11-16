package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/sbom"
)

// Security findings and SBOMs. A step that declares reports.findings has
// its SARIF files collected by its runner once it ends, passed or failed,
// and sent with the result, like test reports; reports.sbom files are kept
// as the step's artifact "sbom" and read back from the blob store. The
// server parses both here (they are untrusted: anyone who can push code
// writes them) and keeps:
//
//   - in the event that ends the attempt, a short summary: counts by
//     severity, the tools, the files and what was wrong with them; for an
//     SBOM the number of components and the commonest licences;
//   - in finding_reports and sbom_reports, the same, one row per attempt;
//   - in findings, finding_rules and sbom_components, the individual
//     findings, the rules they refer to, and the components, written in the
//     same transaction as the event, at most so many per run, pruned oldest
//     run first per project (see pruneFindings).
//
// No snippet of the scanned code is ever kept, and the messages of findings
// that are about secrets are replaced (see package findings): a secret a
// scanner found is never stored or shown, only where it is and which rule
// found it.

// FindingLimits bound what is kept of findings and SBOMs. Zero fields take
// the defaults.
type FindingLimits struct {
	// MaxUploadBytes is how many bytes of SARIF files the server accepts
	// for one step attempt; runners are told. Default 32 MiB.
	MaxUploadBytes int64
	// MaxPerRun is how many findings one run keeps; beyond it only the
	// counts. Default 20,000. MaxPerProject is how many a project keeps
	// over all its runs, the oldest runs' removed first. Default 500,000.
	MaxPerRun     int
	MaxPerProject int
	// MaxComponentsPerRun and MaxComponentsPerProject do the same for SBOM
	// components. Defaults 20,000 and 1,000,000.
	MaxComponentsPerRun     int
	MaxComponentsPerProject int
	// MaxSBOMBytes is how many bytes of an attempt's SBOM files are read to
	// summarise them; the files are kept whole as its artifact anyway.
	// Default 64 MiB.
	MaxSBOMBytes int64
}

// Defaults for FindingLimits.
const (
	DefaultFindingUploadBytes         = 32 << 20
	DefaultFindingsPerRun             = 20_000
	DefaultFindingsPerProject         = 500_000
	DefaultComponentsPerRun           = 20_000
	DefaultComponentsPerProject       = 1_000_000
	DefaultSBOMBytes                  = 64 << 20
	maxFindingFiles                   = 100 // files listed in a summary
	maxSummaryLicences                = 20  // licences in an event's summary
	maxStoredLicences                 = 200 // licences in sbom_reports
	findingParseTime                  = 30 * time.Second
	maxFindingRulesPerAttempt         = 5000
	findingsPerInsert                 = 200
	componentsPerInsert               = 300
	findingRulesPerInsert             = 100
	findingHelpBytes                  = 4096
	findingTagsPerRule                = 20
	sbomPartMax                 int64 = 16 << 20
)

func (l FindingLimits) withDefaults() (FindingLimits, error) {
	if l.MaxUploadBytes == 0 {
		l.MaxUploadBytes = DefaultFindingUploadBytes
	}
	if l.MaxPerRun == 0 {
		l.MaxPerRun = DefaultFindingsPerRun
	}
	if l.MaxPerProject == 0 {
		l.MaxPerProject = DefaultFindingsPerProject
	}
	if l.MaxComponentsPerRun == 0 {
		l.MaxComponentsPerRun = DefaultComponentsPerRun
	}
	if l.MaxComponentsPerProject == 0 {
		l.MaxComponentsPerProject = DefaultComponentsPerProject
	}
	if l.MaxSBOMBytes == 0 {
		l.MaxSBOMBytes = DefaultSBOMBytes
	}
	switch {
	case l.MaxUploadBytes < 0 || l.MaxPerRun < 0 || l.MaxPerProject < 0 || l.MaxComponentsPerRun < 0 ||
		l.MaxComponentsPerProject < 0 || l.MaxSBOMBytes < 0:
		return l, errors.New("finding limits cannot be negative")
	case l.MaxPerProject < l.MaxPerRun:
		return l, fmt.Errorf("findings: max_per_project (%d) must be at least max_per_run (%d)", l.MaxPerProject, l.MaxPerRun)
	case l.MaxComponentsPerProject < l.MaxComponentsPerRun:
		return l, fmt.Errorf("findings: max_components_per_project (%d) must be at least max_components_per_run (%d)",
			l.MaxComponentsPerProject, l.MaxComponentsPerRun)
	}
	return l, nil
}

// FindingLimits returns the limits findings and SBOMs are kept within.
func (e *Engine) FindingLimits() FindingLimits { return e.finds }

// ResultLimit is the largest step result, in bytes of JSON, the server
// accepts from a runner: its test reports and SARIF files (base64 in JSON)
// with room for the rest.
func (e *Engine) ResultLimit() int64 {
	n := e.tests.MaxUploadBytes
	if e.knows(schemaArtifacts) {
		n += e.finds.MaxUploadBytes
	}
	return n*4/3 + 4<<20
}

// FindingCounts counts findings by severity.
type FindingCounts struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	// Suppressed counts results the files marked as suppressed; they are
	// not in Total.
	Suppressed int `json:"suppressed,omitempty"`
	// Truncated says not every finding was kept: a file was cut short, or
	// there were more than a run keeps.
	Truncated bool `json:"truncated,omitempty"`
}

func (c *FindingCounts) add(o FindingCounts) {
	c.Total += o.Total
	c.Critical += o.Critical
	c.High += o.High
	c.Medium += o.Medium
	c.Low += o.Low
	c.Unknown += o.Unknown
	c.Suppressed += o.Suppressed
	c.Truncated = c.Truncated || o.Truncated
}

func (c *FindingCounts) count(s findings.Severity) {
	c.Total++
	switch s {
	case findings.Critical:
		c.Critical++
	case findings.High:
		c.High++
	case findings.Medium:
		c.Medium++
	case findings.Low:
		c.Low++
	default:
		c.Unknown++
	}
}

// FindingFile is one SARIF file of a step attempt, as the server read it.
type FindingFile struct {
	Path     string   `json:"path"`
	Format   string   `json:"format,omitempty"`
	Bytes    int64    `json:"bytes"`
	Findings int      `json:"findings"`
	Tools    []string `json:"tools,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warning  string   `json:"warning,omitempty"`
}

// FindingsSummary is what one step attempt's SARIF files said, as kept in
// its event.
type FindingsSummary struct {
	FindingCounts
	Tools []string      `json:"tools"`
	Files []FindingFile `json:"files"`
	Notes []string      `json:"notes,omitempty"`
	// Stored is how many findings were kept in the findings table.
	Stored int `json:"stored"`
}

func (s *FindingsSummary) note(n string) {
	if n = cleanText(n, maxReportText); n != "" && len(s.Notes) < maxReportNotes {
		s.Notes = append(s.Notes, n)
	}
}

// SBOMFile is one SBOM file of a step attempt, as the server read it.
type SBOMFile struct {
	Path        string `json:"path"`
	Format      string `json:"format,omitempty"`
	SpecVersion string `json:"spec_version,omitempty"`
	Bytes       int64  `json:"bytes"`
	Components  int    `json:"components"`
	Error       string `json:"error,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

// SBOMSummary is what one step attempt's SBOM said, as kept in its event.
type SBOMSummary struct {
	Components int  `json:"components"`
	Stored     int  `json:"stored"`
	Truncated  bool `json:"truncated,omitempty"`
	// Licences counts components by licence: the commonest ones.
	Licences map[string]int `json:"licences"`
	Files    []SBOMFile     `json:"files"`
	Notes    []string       `json:"notes,omitempty"`
	// Artifact is the artifact that keeps the files.
	Artifact string `json:"artifact,omitempty"`
}

func (s *SBOMSummary) note(n string) {
	if n = cleanText(n, maxReportText); n != "" && len(s.Notes) < maxReportNotes {
		s.Notes = append(s.Notes, n)
	}
}

// storedFinding is one finding as it is written to findings.
type storedFinding struct {
	findings.Finding
	file string // the SARIF file it came from
}

// parsedFindings is what an attempt's SARIF files said, before the run's
// limits are applied.
type parsedFindings struct {
	summary  FindingsSummary
	findings []storedFinding
	rules    []findings.Rule
}

// parsedSBOM is what an attempt's SBOM files said.
type parsedSBOM struct {
	summary    SBOMSummary
	licences   map[string]int // every licence counted, for sbom_reports
	components []sbom.Component
}

// parseFindings reads the SARIF files a runner sent. It never fails: what
// cannot be read is described in the summary.
func (e *Engine) parseFindings(in *CollectedReports) *parsedFindings {
	parseSlots <- struct{}{}
	defer func() { <-parseSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), findingParseTime)
	defer cancel()
	out := &parsedFindings{summary: FindingsSummary{Tools: []string{}, Files: []FindingFile{}}}
	s := &out.summary
	for _, n := range in.Notes {
		s.note(n)
	}
	budget := e.finds.MaxUploadBytes
	room := e.finds.MaxPerRun
	seen := map[string]bool{}
	rules := map[string]findings.Rule{}
	tools := map[string]bool{}
	for i, f := range in.Files {
		if i >= maxFindingFiles {
			s.note(fmt.Sprintf("%d more files were sent; only the first %d were read", len(in.Files)-i, maxFindingFiles))
			s.Truncated = true
			break
		}
		ff := FindingFile{Path: nonEmpty(cleanText(f.Path, maxReportText), "(no name)"), Bytes: int64(len(f.Data))}
		if int64(len(f.Data)) > budget {
			ff.Error = fmt.Sprintf("not read: the step's SARIF files came to more than the %s the server accepts", sizeText(e.finds.MaxUploadBytes))
			s.Truncated = true
			s.Files = append(s.Files, ff)
			budget = 0
			continue
		}
		budget -= int64(len(f.Data))
		left := time.Until(deadlineOf(ctx))
		if left <= 0 {
			ff.Error = "not read: reading the step's SARIF files took too long"
			s.Truncated = true
			s.Files = append(s.Files, ff)
			continue
		}
		lim := findings.DefaultLimits()
		lim.MaxFindings = max(room, 1)
		lim.Timeout = min(lim.Timeout, left)
		rep, err := findings.Parse(ctx, f.Data, lim)
		if err != nil {
			ff.Error = cleanText(err.Error(), maxReportText)
			s.Files = append(s.Files, ff)
			continue
		}
		ff.Format, ff.Findings, ff.Tools = rep.Format, rep.Counts.Total, rep.Tools
		ff.Warning = cleanText(rep.Warning, maxReportText)
		if f.Cut {
			ff.Warning = joinText(fmt.Sprintf("the runner kept only the first %s of it", sizeText(int64(len(f.Data)))), ff.Warning)
			s.Truncated = true
		}
		for _, t := range rep.Tools {
			if !tools[t] {
				tools[t] = true
				s.Tools = append(s.Tools, t)
			}
		}
		c := rep.Counts
		s.add(FindingCounts{Total: c.Total, Critical: c.Critical, High: c.High, Medium: c.Medium, Low: c.Low, Unknown: c.Unknown,
			Suppressed: c.Suppressed, Truncated: rep.Truncated})
		for _, r := range rep.Rules {
			if len(rules) < maxFindingRulesPerAttempt {
				rules[r.Tool+"\x00"+r.ID] = r
			}
		}
		for _, fd := range rep.Findings {
			if room <= 0 {
				s.Truncated = true
				break
			}
			// The same finding in two files (one scanner's results written
			// twice, say) is kept once.
			if seen[fd.Fingerprint] {
				continue
			}
			seen[fd.Fingerprint] = true
			out.findings = append(out.findings, storedFinding{Finding: fd, file: ff.Path})
			room--
		}
		s.Files = append(s.Files, ff)
	}
	used := map[string]bool{}
	for _, f := range out.findings {
		used[f.Tool+"\x00"+f.RuleID] = true
	}
	for k, r := range rules {
		if used[k] {
			out.rules = append(out.rules, r)
		}
	}
	sort.Slice(out.rules, func(i, j int) bool {
		if out.rules[i].Tool != out.rules[j].Tool {
			return out.rules[i].Tool < out.rules[j].Tool
		}
		return out.rules[i].ID < out.rules[j].ID
	})
	return out
}

// readSBOM reads the SBOM files a step attempt kept as its artifact sbom,
// from the blob store, within limits. It returns nil if there is none.
func (e *Engine) readSBOM(l leaseRef) *parsedSBOM {
	if e.arts.Store == nil {
		return nil
	}
	a, err := findArtifact(e.db, l.runID, l.stepID, l.attempt, "sbom")
	if err != nil || a.Status != artKept || a.Kind != "sbom" {
		return nil
	}
	files, err := artifactFiles(e.db, a.ID)
	if err != nil {
		e.log.Warn("reading an SBOM artifact's files", "run", l.runID, "step", l.stepID, "err", err)
		return nil
	}
	parseSlots <- struct{}{}
	defer func() { <-parseSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), findingParseTime)
	defer cancel()
	out := &parsedSBOM{summary: SBOMSummary{Files: []SBOMFile{}, Licences: map[string]int{}, Artifact: a.ID}, licences: map[string]int{}}
	s := &out.summary
	budget := e.finds.MaxSBOMBytes
	room := e.finds.MaxComponentsPerRun
	for _, f := range files {
		sf := SBOMFile{Path: cleanText(f.Path, maxReportText), Bytes: f.Size}
		if len(s.Files) >= maxFindingFiles {
			s.note(fmt.Sprintf("only the first %d SBOM files were read", maxFindingFiles))
			s.Truncated = true
			break
		}
		if f.Size > budget || f.Size > sbomPartMax*4 {
			sf.Error = fmt.Sprintf("not read: the SBOM files came to more than the %s the server reads; the file is kept whole in the artifact", sizeText(e.finds.MaxSBOMBytes))
			s.Truncated = true
			s.Files = append(s.Files, sf)
			continue
		}
		budget -= f.Size
		data, err := e.readWhole(ctx, f)
		if err != nil {
			sf.Error = cleanText("could not be read back: "+err.Error(), maxReportText)
			s.Files = append(s.Files, sf)
			continue
		}
		lim := sbom.DefaultLimits()
		lim.MaxComponents = max(room, 1)
		lim.Timeout = min(lim.Timeout, max(time.Until(deadlineOf(ctx)), time.Millisecond))
		rep, err := sbom.Parse(ctx, data, lim)
		if err != nil {
			sf.Error = cleanText(err.Error(), maxReportText)
			s.Files = append(s.Files, sf)
			continue
		}
		sf.Format, sf.SpecVersion, sf.Components, sf.Warning = rep.Format, rep.SpecVersion, rep.Total, cleanText(rep.Warning, maxReportText)
		s.Components += rep.Total
		s.Truncated = s.Truncated || rep.Truncated
		for k, n := range rep.Licenses {
			out.licences[k] += n
		}
		comps := rep.Components
		if len(comps) > room {
			comps = comps[:room]
			s.Truncated = true
		}
		room -= len(comps)
		out.components = append(out.components, comps...)
		s.Files = append(s.Files, sf)
	}
	s.Licences = topLicences(out.licences, maxSummaryLicences)
	out.licences = topLicences(out.licences, maxStoredLicences)
	return out
}

// readWhole reads a stored file into memory.
func (e *Engine) readWhole(ctx context.Context, f artifactFile) ([]byte, error) {
	body, err := e.openBlob(ctx, f, 0)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, f.Size+1))
	if err == nil && int64(len(data)) != f.Size {
		err = fmt.Errorf("it has %d bytes, not %d", len(data), f.Size)
	}
	return data, err
}

// topLicences keeps the n commonest licences, counting the rest as
// "(other)".
func topLicences(all map[string]int, n int) map[string]int {
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(all))
	for k, v := range all {
		list = append(list, kv{k, v})
	}
	slices.SortFunc(list, func(a, b kv) int {
		if a.v != b.v {
			return b.v - a.v
		}
		return strings.Compare(a.k, b.k)
	})
	out := map[string]int{}
	for i, e := range list {
		if i < n || (len(list) == n+1 && i == n) {
			out[e.k] += e.v
		} else {
			out["(other)"] += e.v
		}
	}
	return out
}

// keepFindings applies the run's limit to an attempt's findings and keeps
// them to be stored with the command's events. It returns the summary for
// the event.
func (c *cmd) keepFindings(step string, attempt int, f *parsedFindings) (*FindingsSummary, error) {
	sum := f.summary
	sum.Files = slices.Clone(f.summary.Files)
	sum.Notes = slices.Clone(f.summary.Notes)
	sum.Tools = slices.Clone(f.summary.Tools)
	var kept int
	if err := c.tx.QueryRow(`SELECT COALESCE(SUM(stored), 0) FROM finding_reports WHERE run_id = ?`, c.runID).Scan(&kept); err != nil {
		return nil, err
	}
	room := max(c.e.finds.MaxPerRun-kept, 0)
	list := f.findings
	if len(list) > room {
		list = list[:room]
		sum.Truncated = true
	}
	sum.Stored = len(list)
	if sum.Stored < sum.Total {
		sum.note(fmt.Sprintf("%d of the %d findings were kept with the run, which keeps at most %d; the counts include them all",
			sum.Stored, sum.Total, c.e.finds.MaxPerRun))
	}
	c.s12().findings[stepAttempt{step, attempt}] = &keptFindings{list: list, rules: f.rules}
	return &sum, nil
}

// keepSBOM does the same for an attempt's SBOM components.
func (c *cmd) keepSBOM(step string, attempt int, p *parsedSBOM) (*SBOMSummary, error) {
	sum := p.summary
	sum.Files = slices.Clone(p.summary.Files)
	sum.Notes = slices.Clone(p.summary.Notes)
	var kept int
	if err := c.tx.QueryRow(`SELECT COALESCE(SUM(stored), 0) FROM sbom_reports WHERE run_id = ?`, c.runID).Scan(&kept); err != nil {
		return nil, err
	}
	room := max(c.e.finds.MaxComponentsPerRun-kept, 0)
	list := p.components
	if len(list) > room {
		list = list[:room]
		sum.Truncated = true
	}
	sum.Stored = len(list)
	if sum.Stored < sum.Components {
		sum.note(fmt.Sprintf("%d of the %d components were kept for searching; the run keeps at most %d", sum.Stored, sum.Components,
			c.e.finds.MaxComponentsPerRun))
	}
	c.s12().sbom[stepAttempt{step, attempt}] = &keptSBOM{list: list, licences: p.licences}
	return &sum, nil
}

type keptFindings struct {
	list  []storedFinding
	rules []findings.Rule
}

type keptSBOM struct {
	list     []sbom.Component
	licences map[string]int
}

// persistFindings writes an attempt's finding summary and findings.
func (c *cmd) persistFindings(at int64, step string, attempt int, t *FindingsSummary) error {
	st := c.st
	tools, _ := json.Marshal(t.Tools)
	detail, err := json.Marshal(struct {
		Files []FindingFile `json:"files"`
		Notes []string      `json:"notes,omitempty"`
	}{t.Files, t.Notes})
	if err != nil {
		return err
	}
	if _, err := c.tx.Exec(`INSERT INTO finding_reports(run_id, step_id, attempt, project, run_number, at, total, critical, high, medium,
			low, unknown, suppressed, truncated, stored, pruned, tools, detail)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET at = excluded.at, total = excluded.total, critical = excluded.critical,
			high = excluded.high, medium = excluded.medium, low = excluded.low, unknown = excluded.unknown,
			suppressed = excluded.suppressed, truncated = excluded.truncated, stored = excluded.stored, pruned = 0,
			tools = excluded.tools, detail = excluded.detail`,
		st.ID, step, attempt, st.Project, st.Number, at, t.Total, t.Critical, t.High, t.Medium, t.Low, t.Unknown, t.Suppressed,
		boolInt(t.Truncated), t.Stored, string(tools), string(detail)); err != nil {
		return err
	}
	for _, table := range []string{"findings", "finding_rules"} {
		if _, err := c.tx.Exec(`DELETE FROM `+table+` WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, step, attempt); err != nil {
			return err
		}
	}
	k := c.s12().findings[stepAttempt{step, attempt}]
	if k == nil {
		return nil
	}
	list := k.list
	for len(list) > 0 {
		n := min(len(list), findingsPerInsert)
		var q strings.Builder
		q.WriteString(`INSERT INTO findings(run_id, step_id, attempt, project, run_number, fingerprint, tool, rule_id, severity, level,
			message, file, start_line, end_line, start_column, secret, report_file) VALUES `)
		args := make([]any, 0, n*17)
		for i, f := range list[:n] {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, st.ID, step, attempt, st.Project, st.Number, f.Fingerprint, f.Tool, f.RuleID, string(f.Severity), f.Level,
				f.Message, f.File, f.StartLine, f.EndLine, f.StartColumn, boolInt(f.Secret), f.file)
		}
		if _, err := c.tx.Exec(q.String(), args...); err != nil {
			return err
		}
		list = list[n:]
	}
	rules := k.rules
	for len(rules) > 0 {
		n := min(len(rules), findingRulesPerInsert)
		var q strings.Builder
		q.WriteString(`INSERT INTO finding_rules(run_id, step_id, attempt, project, run_number, tool, rule_id, name, short, full_text,
			help, help_uri, tags) VALUES `)
		args := make([]any, 0, n*13)
		for i, r := range rules[:n] {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?,?,?)")
			tags, _ := json.Marshal(nonNilStrings(r.Tags))
			args = append(args, st.ID, step, attempt, st.Project, st.Number, r.Tool, r.ID, r.Name, r.Short, r.Full, r.Help, r.HelpURI, string(tags))
		}
		if _, err := c.tx.Exec(q.String(), args...); err != nil {
			return err
		}
		rules = rules[n:]
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// persistSBOM writes an attempt's SBOM summary and components.
func (c *cmd) persistSBOM(at int64, step string, attempt int, t *SBOMSummary) error {
	st := c.st
	k := c.s12().sbom[stepAttempt{step, attempt}]
	licences := t.Licences
	if k != nil {
		licences = k.licences
	}
	lic, _ := json.Marshal(licences)
	detail, err := json.Marshal(struct {
		Files    []SBOMFile `json:"files"`
		Notes    []string   `json:"notes,omitempty"`
		Artifact string     `json:"artifact,omitempty"`
	}{t.Files, t.Notes, t.Artifact})
	if err != nil {
		return err
	}
	if _, err := c.tx.Exec(`INSERT INTO sbom_reports(run_id, step_id, attempt, project, run_number, at, components, stored, truncated, pruned,
			licences, detail) VALUES(?,?,?,?,?,?,?,?,?,0,?,?)
		ON CONFLICT(run_id, step_id, attempt) DO UPDATE SET at = excluded.at, components = excluded.components, stored = excluded.stored,
			truncated = excluded.truncated, pruned = 0, licences = excluded.licences, detail = excluded.detail`,
		st.ID, step, attempt, st.Project, st.Number, at, t.Components, t.Stored, boolInt(t.Truncated), string(lic), string(detail)); err != nil {
		return err
	}
	if _, err := c.tx.Exec(`DELETE FROM sbom_components WHERE run_id = ? AND step_id = ? AND attempt = ?`, st.ID, step, attempt); err != nil {
		return err
	}
	if k == nil {
		return nil
	}
	list := k.list
	for len(list) > 0 {
		n := min(len(list), componentsPerInsert)
		var q strings.Builder
		q.WriteString(`INSERT INTO sbom_components(run_id, step_id, attempt, project, run_number, name, name_key, version, type, purl,
			licences) VALUES `)
		args := make([]any, 0, n*11)
		for i, comp := range list[:n] {
			if i > 0 {
				q.WriteString(",")
			}
			q.WriteString("(?,?,?,?,?,?,?,?,?,?,?)")
			args = append(args, st.ID, step, attempt, st.Project, st.Number, comp.Name, strings.ToLower(comp.Name), comp.Version, comp.Type,
				comp.PURL, strings.Join(comp.Licenses, ", "))
		}
		if _, err := c.tx.Exec(q.String(), args...); err != nil {
			return err
		}
		list = list[n:]
	}
	return nil
}

// pruneFindings removes the individual findings and components of each
// project's oldest runs while the project keeps more than its limit; the
// runs' counts stay. It follows pruneTests: several servers may do it at
// once, and each row is removed once.
func (e *Engine) pruneFindings() {
	if !e.knows(schemaArtifacts) {
		return
	}
	for _, k := range []struct {
		reports string
		tables  []string
		limit   int
		what    string
	}{
		{"finding_reports", []string{"findings", "finding_rules"}, e.finds.MaxPerProject, "findings"},
		{"sbom_reports", []string{"sbom_components"}, e.finds.MaxComponentsPerProject, "SBOM components"},
	} {
		projects, err := e.idsOf(`SELECT project FROM `+k.reports+` GROUP BY project HAVING SUM(stored) > ?`, k.limit)
		if err != nil {
			e.log.Error("looking for projects with too many "+k.what, "err", err)
			continue
		}
		for _, p := range projects {
			if err := e.pruneProjectRows(p, k.limit, k.reports, k.tables, k.what); err != nil {
				e.log.Warn("removing a project's oldest "+k.what+"; trying again later", "project", p, "err", err)
			}
		}
	}
}

func (e *Engine) pruneProjectRows(project string, limit int, reports string, tables []string, what string) error {
	rows, err := e.db.Query(`SELECT run_number, SUM(stored) FROM `+reports+` WHERE project = ? AND stored > 0
		GROUP BY run_number ORDER BY run_number DESC`, project)
	if err != nil {
		return err
	}
	cutoff, kept := -1, 0
	for rows.Next() {
		var number, n int
		if err := rows.Scan(&number, &n); err != nil {
			rows.Close()
			return err
		}
		if kept+n > limit {
			cutoff = number
			break
		}
		kept += n
	}
	rows.Close()
	if err := rows.Err(); err != nil || cutoff < 0 {
		return err
	}
	unlock := e.writeLock()
	_, err = e.db.Exec(`UPDATE `+reports+` SET stored = 0, pruned = 1 WHERE project = ? AND run_number <= ? AND (stored > 0 OR pruned = 0)`,
		project, cutoff)
	unlock()
	if err != nil {
		return err
	}
	removed := int64(0)
	for _, table := range tables {
		idCol := "id"
		if table == "finding_rules" {
			// No id of its own: remove by run.
			unlock := e.writeLock()
			res, err := e.db.Exec(`DELETE FROM finding_rules WHERE project = ? AND run_number <= ?`, project, cutoff)
			unlock()
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			removed += n
			continue
		}
		for {
			unlock := e.writeLock()
			res, err := e.db.Exec(`DELETE FROM `+table+` WHERE `+idCol+` IN (SELECT `+idCol+` FROM `+table+` WHERE project = ? AND run_number <= ?
				ORDER BY `+idCol+` LIMIT ?)`, project, cutoff, pruneBatch)
			unlock()
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			removed += n
			if n < pruneBatch {
				break
			}
		}
	}
	if removed > 0 {
		e.log.Info("removed the oldest "+what+" of a project, keeping their counts", "project", project, "up_to_run", cutoff,
			"rows", removed, "limit", limit)
	}
	return nil
}
