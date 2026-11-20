package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testreport"
)

// Schema 18: richer test results, test attachments, coverage and HTML
// reports.
//
// Test results keep, beside what schema 10 keeps, their tags, the variant
// they ran as (a Playwright project, a browser), and in test_cases.extra
// their steps, attachments, retries and properties, bounded. Attachments are
// files: those a report names by path must lie inside the step's
// reports.tests.attachments directory, which the runner keeps as the
// artifact test-attachments (regular files only, never through a symbolic
// link, see internal/executor); those a report carries inline (Cucumber
// embeddings, Playwright bodies) are stored by the server as the artifact
// test-embedded, through the same blob store and quotas as any artifact.
// An attachment is shown only from one of those two artifacts: a report
// cannot point at anything else.
//
// Coverage reports are parsed like test reports: a summary in the event
// that ends the attempt (Payload.Coverage), coverage_reports derived from
// it, and each file's counts in coverage_files beside the events.
// fail_under fails a step whose coverage is lower.
//
// HTML reports are artifacts of kind html-report (report-<name>), served
// sandboxed by the API (see internal/api/hosted.go).

// FeatureCoverage is what a runner that collects coverage reports says it
// can do. A step that declares coverage is only leased to such a runner:
// an older one would run it without collecting them, and fail_under would
// then fail it.
const FeatureCoverage = "coverage"

// Kinds of the artifacts schema 18 keeps.
const (
	KindTestAttachments = "test-attachments"
	KindTestEmbedded    = "test-embedded"
	KindHTMLReport      = "html-report"
)

// ReportOptions bound what schema 18 keeps. Zero fields take the defaults.
type ReportOptions struct {
	// AttachmentFiles and AttachmentBytes bound a step's attachments
	// directory, kept whole or not at all. Defaults 2000 files, 512 MiB.
	AttachmentFiles int
	AttachmentBytes int64
	// HTMLFiles and HTMLBytes bound one HTML report. Defaults 5000 files,
	// 256 MiB.
	HTMLFiles int
	HTMLBytes int64
	// CoverageUploadBytes is how many bytes of coverage reports the server
	// accepts per step attempt. Default 24 MiB.
	CoverageUploadBytes int64
	// CoverageFilesPerRun and CoverageFilesPerProject bound how many
	// source files' counts are kept. Defaults 20,000 and 2,000,000.
	CoverageFilesPerRun     int
	CoverageFilesPerProject int
}

// Defaults for ReportOptions.
const (
	DefaultAttachmentFiles         = 2000
	DefaultAttachmentBytes         = 512 << 20
	DefaultHTMLFiles               = 5000
	DefaultHTMLBytes               = 256 << 20
	DefaultCoverageUploadBytes     = 24 << 20
	DefaultCoverageFilesPerRun     = 20_000
	DefaultCoverageFilesPerProject = 2_000_000
)

func (o ReportOptions) withDefaults() (ReportOptions, error) {
	if o.AttachmentFiles == 0 {
		o.AttachmentFiles = DefaultAttachmentFiles
	}
	if o.AttachmentBytes == 0 {
		o.AttachmentBytes = DefaultAttachmentBytes
	}
	if o.HTMLFiles == 0 {
		o.HTMLFiles = DefaultHTMLFiles
	}
	if o.HTMLBytes == 0 {
		o.HTMLBytes = DefaultHTMLBytes
	}
	if o.CoverageUploadBytes == 0 {
		o.CoverageUploadBytes = DefaultCoverageUploadBytes
	}
	if o.CoverageFilesPerRun == 0 {
		o.CoverageFilesPerRun = DefaultCoverageFilesPerRun
	}
	if o.CoverageFilesPerProject == 0 {
		o.CoverageFilesPerProject = DefaultCoverageFilesPerProject
	}
	switch {
	case o.AttachmentFiles < 0 || o.AttachmentBytes < 0 || o.HTMLFiles < 0 || o.HTMLBytes < 0 || o.CoverageUploadBytes < 0 ||
		o.CoverageFilesPerRun < 0 || o.CoverageFilesPerProject < 0:
		return o, errors.New("report limits cannot be negative")
	case o.AttachmentFiles > MaxArtifactFiles || o.HTMLFiles > MaxArtifactFiles:
		return o, fmt.Errorf("reports: an attachments directory or HTML report holds at most %d files", MaxArtifactFiles)
	case o.CoverageFilesPerProject < o.CoverageFilesPerRun:
		return o, fmt.Errorf("reports: max_coverage_files_per_project (%d) must be at least max_coverage_files_per_run (%d)",
			o.CoverageFilesPerProject, o.CoverageFilesPerRun)
	}
	return o, nil
}

// CheckReportOptions fills in and checks report options, as Open does.
func CheckReportOptions(o ReportOptions) (ReportOptions, error) { return o.withDefaults() }

// ReportOptions returns the options schema 18 keeps reports within.
func (e *Engine) ReportOptions() ReportOptions { return e.reps }

// needsCoverage reports whether a step needs a runner that collects
// coverage reports.
func needsCoverage(def *pipeline.Step) bool {
	return def != nil && def.Reports != nil && len(def.Reports.Coverage) > 0
}

// reportArtifacts are the artifacts a step's reports keep (schema 18): its
// attachments directory and its HTML reports.
func (c *cmd) reportArtifacts(def *pipeline.Step) []JobArtifact {
	if !c.e.knows(schemaReports) || def.Reports == nil {
		return nil
	}
	var out []JobArtifact
	o := c.e.reps
	if d := def.Reports.TestAttachments; d != "" {
		out = append(out, JobArtifact{Name: pipeline.TestAttachmentsArtifact, Paths: []string{d}, When: pipeline.ArtifactAlways,
			MaxFiles: o.AttachmentFiles, MaxBytes: o.AttachmentBytes, Label: "Test attachments"})
	}
	for _, h := range def.Reports.HTML {
		out = append(out, JobArtifact{Name: pipeline.HTMLReportPrefix + h.Name, Paths: []string{h.Path}, When: pipeline.ArtifactAlways,
			MaxFiles: o.HTMLFiles, MaxBytes: o.HTMLBytes, Label: "HTML report " + h.Name})
	}
	return out
}

// reportArtifactKind says which kind of artifact a name is in a step, and
// its limits, or "" if it is none of schema 18's.
func (e *Engine) reportArtifactKind(def *pipeline.Step, name string) (kind string, files int, bytes int64) {
	if !e.knows(schemaReports) || def == nil || def.Reports == nil {
		return "", 0, 0
	}
	r := def.Reports
	switch {
	case name == pipeline.TestAttachmentsArtifact && r.TestAttachments != "":
		return KindTestAttachments, e.reps.AttachmentFiles, e.reps.AttachmentBytes
	case name == pipeline.TestEmbeddedArtifact && len(r.Tests) > 0:
		return KindTestEmbedded, maxEmbeddedFiles, maxEmbeddedBytes
	case strings.HasPrefix(name, pipeline.HTMLReportPrefix):
		for _, h := range r.HTML {
			if pipeline.HTMLReportPrefix+h.Name == name {
				return KindHTMLReport, e.reps.HTMLFiles, e.reps.HTMLBytes
			}
		}
	}
	return "", 0, 0
}

// Bounds on what the server stores of inline attachments for one attempt.
const (
	maxEmbeddedFiles = 1000
	maxEmbeddedBytes = 64 << 20
)

// ---- test attachments ----

// TestAttachment is an attachment of a test result, as kept: a file of the
// artifact test-attachments or test-embedded of the same step attempt.
type TestAttachment struct {
	Name string `json:"name"`
	// Kind is what the UI shows it as: image, video, trace, text, html or
	// file. It is worked out from the declared type and the name; what the
	// server serves is decided again from the content itself.
	Kind        string `json:"kind"`
	ContentType string `json:"content_type,omitempty"` // as the report declared it
	Size        int64  `json:"size,omitempty"`
	Artifact    string `json:"artifact,omitempty"`
	Path        string `json:"path,omitempty"` // in the artifact
	// Problem says why it cannot be shown: it is outside the attachments
	// directory, was not kept, or was too large.
	Problem string `json:"problem,omitempty"`
}

// caseExtra is what test_cases.extra holds.
type caseExtra struct {
	Retries     int                   `json:"retries,omitempty"`
	Attempts    []testreport.Attempt  `json:"attempts,omitempty"`
	Steps       []testreport.Step     `json:"steps,omitempty"`
	Attachments []TestAttachment      `json:"attachments,omitempty"`
	Properties  []testreport.Property `json:"properties,omitempty"`
	// StepsCut says steps were left out to keep the result small.
	StepsCut bool `json:"steps_cut,omitempty"`
}

// Bounds on test_cases.extra.
const (
	maxExtraBytes        = 64 << 10 // one result
	maxExtraAttemptBytes = 16 << 20 // all of an attempt's results
)

// attachmentKind guesses what an attachment is from its declared type and
// name. It is only a hint for the UI.
func attachmentKind(name, contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ext := strings.ToLower(path.Ext(name))
	base := strings.ToLower(path.Base(name))
	switch {
	case ct == "image/svg+xml" || ext == ".svg":
		return "file" // SVG can carry script: never shown inline
	case strings.HasPrefix(ct, "image/") || slices.Contains([]string{".png", ".jpg", ".jpeg", ".gif", ".webp"}, ext):
		return "image"
	case strings.HasPrefix(ct, "video/") || slices.Contains([]string{".webm", ".mp4"}, ext):
		return "video"
	case (ext == ".zip" || ct == "application/zip") && strings.Contains(base, "trace"):
		return "trace"
	case ct == "text/html" || ext == ".html" || ext == ".htm":
		return "html"
	case strings.HasPrefix(ct, "text/") || ct == "application/json" || slices.Contains([]string{".txt", ".log", ".json", ".md", ".csv"}, ext):
		return "text"
	}
	return "file"
}

// extFor is the file extension the server gives an inline attachment it
// stores, from its content.
func extFor(data []byte, kind string) string {
	switch sniffed := SniffAttachment(data); sniffed {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/webm":
		return ".webm"
	case "video/mp4":
		return ".mp4"
	case "text/plain; charset=utf-8":
		return ".txt"
	}
	if kind == "trace" {
		return ".zip"
	}
	return ".bin"
}

// SniffAttachment says what an attachment's content is, for serving: one of
// the raster image and video types that browsers show without running
// anything, "text/plain; charset=utf-8" for text, or
// "application/octet-stream" for anything else (HTML, SVG, XML, PDF,
// archives), which is only ever offered as a download.
func SniffAttachment(head []byte) string {
	head = head[:min(len(head), 512)]
	switch {
	case len(head) >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		return "video/webm"
	case len(head) >= 12 && string(head[4:8]) == "ftyp":
		return "video/mp4"
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	}
	switch ct := http.DetectContentType(head); ct {
	case "image/png", "image/jpeg", "image/gif":
		return ct
	case "text/plain; charset=utf-8":
		return ct
	}
	return "application/octet-stream"
}

// resolver finds the files test reports attach to their results.
type resolver struct {
	dir       string                  // the step's attachments directory, or ""
	workspace string                  // where the step saw its workspace, from the runner
	kept      map[string]artifactFile // the files of test-attachments, by path
	problem   string                  // why test-attachments has no files
}

// resolve maps a path a report named to a file of test-attachments. The
// path is taken relative to the report file's directory, or to the
// workspace; an absolute path must be inside the workspace. Whatever it
// says, only a file of the kept artifact is ever used: those are regular
// files that were inside the attachments directory, never reached through
// a symbolic link.
func (rv *resolver) resolve(name, reportFile string) (string, *artifactFile, string) {
	p := strings.ReplaceAll(name, "\\", "/")
	if strings.ContainsRune(p, 0) || p == "" {
		return "", nil, "the report names no usable path"
	}
	if rv.dir == "" {
		return "", nil, "not kept: the step declares no attachments directory (reports.tests.attachments)"
	}
	var candidates []string
	switch {
	case strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && p[2] == '/'):
		for _, ws := range []string{rv.workspace, "/workspace"} {
			ws = strings.TrimRight(strings.ReplaceAll(ws, "\\", "/"), "/")
			if ws != "" && strings.HasPrefix(p, ws+"/") {
				candidates = append(candidates, strings.TrimPrefix(p, ws+"/"))
			}
		}
		if len(candidates) == 0 {
			return "", nil, "not shown: " + clipAt(p, 120) + " is outside the workspace"
		}
	default:
		if d := path.Dir(reportFile); d != "." && d != "" {
			candidates = append(candidates, path.Join(d, p))
		}
		candidates = append(candidates, p)
	}
	outside, inside := "", ""
	for _, c := range candidates {
		c = path.Clean(c)
		if c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
			outside = c
			continue
		}
		if !strings.HasPrefix(c, rv.dir+"/") {
			outside = c
			continue
		}
		inside = c
		if f, ok := rv.kept[c]; ok {
			return c, &f, ""
		}
	}
	if inside == "" {
		return "", nil, "not shown: " + clipAt(outside, 120) + " is outside the attachments directory " + rv.dir
	}
	if rv.problem != "" {
		return "", nil, "not kept: " + rv.problem
	}
	return "", nil, "not kept: " + clipAt(inside, 120) + " is not among the files kept from " + rv.dir
}

// newResolver reads what the runner kept of the attachments directory.
func (e *Engine) newResolver(l leaseRef, def *pipeline.Step, workspace string) *resolver {
	rv := &resolver{workspace: workspace, kept: map[string]artifactFile{}}
	if def == nil || def.Reports == nil || def.Reports.TestAttachments == "" || !e.knows(schemaReports) {
		return rv
	}
	rv.dir = def.Reports.TestAttachments
	if e.arts.Store == nil {
		rv.problem = "this server keeps no artifacts: " + e.arts.StoreProblem
		return rv
	}
	a, err := findArtifact(e.db, l.runID, l.stepID, l.attempt, pipeline.TestAttachmentsArtifact)
	switch {
	case errors.Is(err, ErrNotFound):
		rv.problem = "the attachments directory " + rv.dir + " was not kept (it was empty or over the limits; the step's log says why)"
		return rv
	case err != nil:
		rv.problem = "the attachments could not be read"
		e.log.Warn("reading a step's test attachments", "run", l.runID, "step", l.stepID, "err", err)
		return rv
	case a.Status != artKept:
		rv.problem = "the attachments directory is " + a.statusText()
		return rv
	}
	files, err := artifactFiles(e.db, a.ID)
	if err != nil {
		rv.problem = "the attachments could not be read"
		e.log.Warn("reading a step's test attachments", "run", l.runID, "step", l.stepID, "err", err)
		return rv
	}
	for _, f := range files {
		rv.kept[f.Path] = f
	}
	return rv
}

// embedded gathers the attachments reports carry inline, to store as one
// artifact.
type embedded struct {
	files map[string][]byte // path in the artifact → content
	bytes int64
	order []string
}

func (em *embedded) add(data []byte, kind string) (string, string) {
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:12]) + extFor(data, kind)
	if _, ok := em.files[name]; ok {
		return name, ""
	}
	if len(em.files) >= maxEmbeddedFiles {
		return "", fmt.Sprintf("not kept: the step's reports carry more than %d attachments", maxEmbeddedFiles)
	}
	if em.bytes+int64(len(data)) > maxEmbeddedBytes {
		return "", fmt.Sprintf("not kept: the step's reports carry more than %s of attachments", sizeText(maxEmbeddedBytes))
	}
	if em.files == nil {
		em.files = map[string][]byte{}
	}
	em.files[name] = data
	em.order = append(em.order, name)
	em.bytes += int64(len(data))
	return name, ""
}

// resolveAttachments works out, for each result of an attempt, its attachments
// and the rest of what test_cases.extra keeps. It runs before the
// transaction that ends the attempt, while the lease is still held, because
// it stores inline attachments through the artifact upload.
func (e *Engine) resolveAttachments(ctx context.Context, leaseID string, l leaseRef, def *pipeline.Step, t *parsedTests, workspace string) {
	if t == nil || !e.knows(schemaReports) {
		return
	}
	rv := e.newResolver(l, def, workspace)
	em := &embedded{}
	type pending struct {
		c   int
		a   int
		key string
	}
	var waiting []pending
	shown, missing := 0, 0
	for i := range t.cases {
		sc := &t.cases[i]
		for j, a := range sc.Attachments {
			ta := TestAttachment{Name: a.Name, ContentType: a.ContentType, Kind: attachmentKind(nonEmpty(a.Name, a.Path), a.ContentType)}
			switch {
			case a.Problem != "":
				ta.Problem, ta.Size = a.Problem, a.Size
			case a.Data != nil:
				ta.Kind = attachmentKind(nonEmpty(a.Name, "x"+extFor(a.Data, ta.Kind)), a.ContentType)
				if s := SniffAttachment(a.Data); strings.HasPrefix(s, "image/") || strings.HasPrefix(s, "video/") {
					ta.Kind = strings.SplitN(s, "/", 2)[0]
				}
				name, problem := em.add(a.Data, ta.Kind)
				ta.Size = int64(len(a.Data))
				if problem != "" {
					ta.Problem = problem
				} else {
					ta.Artifact, ta.Path = pipeline.TestEmbeddedArtifact, name
					waiting = append(waiting, pending{i, j, name})
				}
			default:
				rel, f, problem := rv.resolve(a.Path, sc.file)
				if problem != "" {
					ta.Problem = problem
				} else {
					ta.Artifact, ta.Path, ta.Size = pipeline.TestAttachmentsArtifact, rel, f.Size
					if ta.Name == "" {
						ta.Name = path.Base(rel)
					}
				}
			}
			if ta.Name == "" {
				ta.Name = "attachment " + fmt.Sprint(j+1)
			}
			if ta.Problem != "" {
				missing++
			} else {
				shown++
			}
			sc.attach = append(sc.attach, ta)
			sc.Attachments[j].Data = nil
		}
	}
	if len(em.files) > 0 {
		if err := e.storeEmbedded(ctx, leaseID, em); err != nil {
			problem := cleanText("not kept: "+err.Error(), maxReportText)
			for _, w := range waiting {
				ta := &t.cases[w.c].attach[w.a]
				ta.Artifact, ta.Path, ta.Problem = "", "", problem
			}
			shown -= len(waiting)
			missing += len(waiting)
			t.summary.note("the attachments carried inside the reports could not be kept: " + err.Error())
		}
	}
	t.summary.Attachments, t.summary.AttachmentsMissing = shown, missing
	if missing > 0 && rv.problem != "" {
		t.summary.note("test attachments: " + rv.problem)
	}
}

// storeEmbedded keeps inline attachments as the artifact test-embedded of
// the attempt, through the same upload as a runner's artifacts.
func (e *Engine) storeEmbedded(ctx context.Context, leaseID string, em *embedded) error {
	if e.arts.Store == nil {
		return errors.New("this server keeps no artifacts: " + e.arts.StoreProblem)
	}
	in := BeginArtifact{Name: pipeline.TestEmbeddedArtifact}
	for _, name := range em.order {
		data := em.files[name]
		sum := sha256.Sum256(data)
		in.Files = append(in.Files, ArtifactFileInfo{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Mode: 0o644})
	}
	begun, err := e.BeginArtifact(leaseID, in)
	if err != nil {
		return err
	}
	for _, f := range begun.Files {
		if f.Done {
			continue
		}
		data := em.files[f.Path]
		for off := f.Received; off < int64(len(data)); off += ArtifactChunkBytes {
			chunk := data[off:min(off+ArtifactChunkBytes, int64(len(data)))]
			sum := sha256.Sum256(chunk)
			if _, err := e.PutArtifactChunk(ctx, leaseID, begun.ID, f.Path, off, hex.EncodeToString(sum[:]), chunk); err != nil {
				return err
			}
		}
	}
	_, err = e.CommitArtifact(ctx, leaseID, begun.ID)
	return err
}

// extraOf makes what test_cases.extra keeps of a result, within
// maxExtraBytes, or "" if there is nothing.
func extraOf(sc *storedCase, budget *int) string {
	x := caseExtra{Retries: sc.Retries, Attempts: sc.Attempts, Steps: sc.Steps, Attachments: sc.attach, Properties: sc.Properties}
	if x.Retries == 0 && len(x.Attempts) == 0 && len(x.Steps) == 0 && len(x.Attachments) == 0 && len(x.Properties) == 0 {
		return ""
	}
	failing := sc.Status == testreport.Failed || sc.Status == testreport.Errored || sc.Flaky
	data, _ := json.Marshal(x)
	if len(data) > maxExtraBytes || (!failing && len(data) > *budget/4) {
		// Steps are the bulk: keep their outline without texts and bodies.
		x.Steps = slices.Clone(x.Steps)
		for i := range x.Steps {
			s := &x.Steps[i]
			s.Text = cutText(s.Text, 1024)
			if s.HTTP != nil {
				h := *s.HTTP
				h.RequestBody, h.ResponseBody = cutText(h.RequestBody, 2048), cutText(h.ResponseBody, 2048)
				s.HTTP = &h
			}
		}
		x.StepsCut = true
		data, _ = json.Marshal(x)
	}
	for len(data) > maxExtraBytes && len(x.Steps) > 0 {
		x.Steps = x.Steps[:len(x.Steps)/2]
		x.StepsCut = true
		data, _ = json.Marshal(x)
	}
	if len(data) > maxExtraBytes {
		return ""
	}
	if len(data) > *budget {
		if !failing {
			return ""
		}
		x.Steps, x.StepsCut = nil, true
		if data, _ = json.Marshal(x); len(data) > *budget {
			return ""
		}
	}
	*budget -= len(data)
	return string(data)
}

func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utfStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func utfStart(b byte) bool { return b&0xC0 != 0x80 }

// tagsColumn writes tags for test_cases.tags: |a|b|, so one can be found
// with LIKE '%|a|%'.
func tagsColumn(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('|')
	for _, t := range tags {
		b.WriteString(strings.ReplaceAll(strings.ToLower(t), "|", "/"))
		b.WriteByte('|')
	}
	return b.String()
}

func tagsFromColumn(s string) []string {
	s = strings.Trim(s, "|")
	if s == "" {
		return nil
	}
	return strings.Split(s, "|")
}

// keepsArtifacts reports whether a step attempt may keep artifacts, its
// own or its reports': the runner's, and test-embedded, which the server
// keeps for a step with test reports (schema 18).
func (c *cmd) keepsArtifacts(def *pipeline.Step) bool {
	return needsArtifacts(def) || (c.e.knows(schemaReports) && def != nil && def.Reports != nil && len(def.Reports.Tests) > 0)
}

// artifactWhat names an artifact kind in messages.
func artifactWhat(kind string) string {
	switch kind {
	case KindTestAttachments:
		return "the attachments directory"
	case KindTestEmbedded:
		return "the attachments carried in the reports"
	case KindHTMLReport:
		return "an HTML report"
	}
	return "an artifact"
}

// ---- coverage thresholds ----

// coverageShortfall says why a step's coverage fails its fail_under, or ""
// if it does not.
func coverageShortfall(th *pipeline.CoverageThreshold, s *CoverageSummary) string {
	if th == nil {
		return ""
	}
	if s == nil || s.ReadFiles == 0 {
		return "fail_under could not be checked: no coverage report could be read (the step's Coverage tab says why)"
	}
	var short []string
	check := func(what string, c CoverageCounter, want float64) {
		if want <= 0 {
			return
		}
		if c.Total == 0 {
			short = append(short, fmt.Sprintf("the reports count no %s, so %s coverage of %s%% could not be checked", what, what, fmtPercent(want)))
			return
		}
		got := 100 * float64(c.Covered) / float64(c.Total)
		if got+1e-9 < want {
			short = append(short, fmt.Sprintf("%s coverage is %s%% (%d of %d), under the %s%% this step requires",
				what, fmtPercent(math.Floor(got*100)/100), c.Covered, c.Total, fmtPercent(want)))
		}
	}
	unit := "line"
	if s.Unit == "statements" {
		unit = "statement"
	}
	check(unit, s.Lines, th.Lines)
	check("branch", s.Branches, th.Branches)
	check("function", s.Functions, th.Functions)
	if len(short) == 0 {
		return ""
	}
	return "Coverage too low (fail_under): " + strings.Join(short, "; ")
}

func fmtPercent(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// reportsDeadline bounds the work schema 18 does before recording an
// attempt's end.
const reportsDeadline = 2 * time.Minute

// ---- wiring into Finish ----

// prepared18 is what Finish works out before its transaction for schema
// 18: the attempt's coverage, parsed.
type prepared18 struct {
	coverage *parsedCoverage
}

// prepare18 parses the coverage reports a runner sent and works out the
// attachments of the test results, storing those carried inline. It runs
// while the lease is still held, before the transaction that ends the
// attempt; it never fails.
func (e *Engine) prepare18(leaseID string, l leaseRef, res Result, tests *parsedTests) *prepared18 {
	p := &prepared18{}
	if !e.knows(schemaReports) || (res.Outcome != "succeeded" && res.Outcome != "failed" && res.Outcome != "canceled") {
		return p
	}
	st, err := e.runState(l.runID)
	if err != nil {
		return p
	}
	def := st.Pipeline.Step(l.stepID)
	if res.Coverage != nil && needsCoverage(def) {
		p.coverage = e.parseCoverage(res.Coverage)
	}
	if tests != nil {
		ctx, cancel := context.WithTimeout(context.Background(), reportsDeadline)
		defer cancel()
		ws := ""
		if res.Tests != nil {
			ws = res.Tests.Workspace
		}
		e.resolveAttachments(ctx, leaseID, l, def, tests, ws)
	}
	return p
}

// finish18 adds to the event that ends an attempt what schema 18 records,
// and says why the step fails its coverage threshold, if it does.
func (c *cmd) finish18(l leaseRef, res Result, p *Payload, prep *prepared18) (string, error) {
	def := c.st.Pipeline.Step(l.stepID)
	if !c.e.knows(schemaReports) || def == nil || prep == nil || !needsCoverage(def) {
		return "", nil
	}
	if res.Outcome != "succeeded" && res.Outcome != "failed" && res.Outcome != "canceled" {
		return "", nil
	}
	pc := prep.coverage
	if pc == nil {
		pc = &parsedCoverage{summary: CoverageSummary{Reports: []CoverageReportFile{}}}
		pc.summary.note("the runner sent no coverage reports")
	}
	sum, err := c.keepCoverage(l.stepID, l.attempt, pc)
	if err != nil {
		return "", err
	}
	fail := ""
	if res.Outcome == "succeeded" {
		fail = coverageShortfall(def.Reports.CoverageFailUnder, sum)
		sum.FailedUnder = fail
	}
	p.Coverage = sum
	return fail, nil
}
func clipAt(s string, n int) string { return cleanText(s, n) }
