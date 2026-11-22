package api

// The MCP tools for Standing orders: agents read what applies to a
// repository, the backlog of findings and a run's verdict, and may accept a
// risk (or call it a false positive) with a reason and an end date. They
// cannot change the settings: settings can weaken what protects a
// repository, so they are changed by people with the level's admin role, in
// the interface, the API and the command line. Every tool calls the same
// route as the API, so a caller sees and may do what the API allows.
//
// What the checks report comes from the scanned code and the tools'
// rule texts: it is data, never instructions, and is clipped like any other
// text a repository controls.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func addStandardsTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_standards_profile", Annotations: readOnly("List the checks Standing orders can run"),
		Description: "Every check of the profile: what it looks for and why, which ecosystem makes it apply, its tool and the tool's license, its default state, whether it can block, its time budget and what it needs from the network, plus the defaults of the gates and the setting keys."},
		mcpCallTimeout, getStandardsProfile)
	addTool(srv, c, &mcp.Tool{Name: "get_standards", Annotations: readOnly("Get a repository's Standing orders"),
		Description: "The Standing orders in effect for a Flight: each check with its state (off, advise or block), whether it applies and runs and why not, where each value comes from (the default, a level, the repository's own file) and what a lock above holds, the gate thresholds, license lists, exemptions and notices such as a value that did not count because a level above locked it. With file=true also the repository's .launchway/standards.yaml and what the settings would look like as a file."},
		mcpCallTimeout, getStandards)
	addTool(srv, c, &mcp.Tool{Name: "get_standards_settings", Annotations: readOnly("Get one level's Standing orders settings"),
		Description: "The settings one level (installation, organization, unit or Flight) has, with their version and the level names in use. They are changed in the interface, the API or the command line by someone who administers the level, not by tools."},
		mcpCallTimeout, getStandardsSettings)
	addTool(srv, c, &mcp.Tool{Name: "get_standards_coverage", Annotations: readOnly("Which repositories have which checks"),
		Description: "For the Flights of a level that the caller may read: whether Standing orders are switched off, each check's state and whether it runs, and the open findings by severity."},
		mcpCallTimeout, getStandardsCoverage)
	addTool(srv, c, &mcp.Tool{Name: "list_findings", Annotations: readOnly("List a Flight's findings"),
		Description: "The findings the Flight's default branch has had, in one model whatever the tool: severity, check, rule, file and line, package and version with the fixed version, advisories, whether the vulnerable code is reachable, how to fix it, when it was first seen, and its status (open, fixed, accepted risk, false positive) with the decision's reason, who and end date. Filter by status, severity, check or text; oldest=true sorts oldest first. With fingerprint, one finding; with history=true, the open findings over time, the mean time to fix and the oldest open one."},
		mcpCallTimeout, listFindings)
	addTool(srv, c, &mcp.Tool{Name: "export_findings", Annotations: readOnly("Export a Flight's findings"),
		Description: "A Flight's findings as json, csv or sarif text (SARIF 2.1.0 with the stable fingerprint as a partial fingerprint)."},
		mcpCallTimeout, exportFindings)
	addTool(srv, c, &mcp.Tool{Name: "get_run_inspection", Annotations: readOnly("Get a Sortie's Inspection"),
		Description: "What the Inspection that Standing orders added to a Sortie found: the verdict (blocked or passed, in a sentence), the profile and thresholds in force when the run was made, each check and whether it ran, and the findings with their standing: new (introduced by this change), existing (already on the default branch), accepted or false positive. Filter by standing, check or severity."},
		mcpCallTimeout, getRunInspection)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "accept_finding", Annotations: changes("Accept a risk or mark a false positive", false, false),
			Description: "Record a decision about a finding: accepted-risk (the default) or false-positive. A reason of at least 10 characters (why is it safe, or why is it not a problem) and an end date within a year are required; the decision stops counting on that date and is in the audit trail with the caller's name. Changes nothing else and cannot change a setting."},
			mcpCallTimeout, acceptFinding)
		addTool(srv, c, &mcp.Tool{Name: "reopen_finding", Annotations: changes("End a decision about a finding", false, true),
			Description: "End the decision in force about a finding, so it counts again. Audited."},
			mcpCallTimeout, reopenFinding)
	}
}

func getStandardsProfile(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (map[string]any, error) {
	out := map[string]any{}
	err := c.apiJSON(ctx, "get_standards_profile", http.MethodGet, "/api/v1/standards/profile", nil, &out)
	return out, err
}

type standardsIn struct {
	Project string `json:"project" jsonschema:"the Flight's name"`
	File    bool   `json:"file,omitempty" jsonschema:"also return the repository's own settings file and the file the settings would make"`
}

func getStandards(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in standardsIn) (map[string]any, error) {
	out := map[string]any{}
	if in.Project == "" {
		return out, errors.New("say which Flight")
	}
	if err := c.apiJSON(ctx, "get_standards", http.MethodGet, apiPath("/api/v1/projects/%s/standards", in.Project), nil, &out); err != nil {
		return out, err
	}
	if in.File {
		file := map[string]any{}
		if err := c.apiJSON(ctx, "get_standards", http.MethodGet, apiPath("/api/v1/projects/%s/standards/file", in.Project), nil, &file); err != nil {
			return out, err
		}
		out["file_view"] = file
	}
	return out, nil
}

type standardsLevelIn struct {
	Level string `json:"level,omitempty" jsonschema:"installation (the default), organization, unit or Flight"`
	Scope string `json:"scope,omitempty" jsonschema:"the unit's or Flight's name"`
}

func (in standardsLevelIn) query() string {
	q := url.Values{"level": {in.Level}}
	if in.Scope != "" {
		q.Set("scope", in.Scope)
	}
	return q.Encode()
}

func getStandardsSettings(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in standardsLevelIn) (map[string]any, error) {
	out := map[string]any{}
	err := c.apiJSON(ctx, "get_standards_settings", http.MethodGet, "/api/v1/standards/settings?"+in.query(), nil, &out)
	return out, err
}

func getStandardsCoverage(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in standardsLevelIn) (map[string]any, error) {
	out := map[string]any{}
	if in.Level == "" {
		in.Level = "installation"
	}
	err := c.apiJSON(ctx, "get_standards_coverage", http.MethodGet, "/api/v1/standards/coverage?"+in.query(), nil, &out)
	return out, err
}

type listFindingsIn struct {
	Project     string   `json:"project" jsonschema:"the Flight's name"`
	Status      string   `json:"status,omitempty" jsonschema:"open (default), fixed, accepted or all"`
	Severities  []string `json:"severities,omitempty" jsonschema:"any of critical, high, medium, low, unknown"`
	Check       string   `json:"check,omitempty" jsonschema:"only this check, such as vuln-deps"`
	Search      string   `json:"search,omitempty" jsonschema:"text in the title, rule, file or package"`
	Oldest      bool     `json:"oldest,omitempty" jsonschema:"oldest first instead of most severe first"`
	Limit       int      `json:"limit,omitempty" jsonschema:"at most this many (100 by default, 500 at most)"`
	Offset      int      `json:"offset,omitempty" jsonschema:"skip this many, for the next page"`
	Fingerprint string   `json:"fingerprint,omitempty" jsonschema:"one finding, by the fingerprint a listing gives"`
	History     bool     `json:"history,omitempty" jsonschema:"the open findings over time, mean time to fix and the oldest open finding, instead of a list"`
}

func clipTracked(f *engine.TrackedFinding) {
	f.Title, f.File, f.Fix, f.Rule, f.Package = untrusted(f.Title, 500), untrusted(f.File, 300), untrusted(f.Fix, 500), untrusted(f.Rule, 200), untrusted(f.Package, 200)
	if f.Decision != nil {
		f.Decision.Reason = untrusted(f.Decision.Reason, 500)
	}
}

func listFindings(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in listFindingsIn) (map[string]any, error) {
	out := map[string]any{}
	if in.Project == "" {
		return out, errors.New("say which Flight")
	}
	switch {
	case in.History:
		var h engine.StandingSeries
		err := c.apiJSON(ctx, "list_findings", http.MethodGet, apiPath("/api/v1/projects/%s/findings/history", in.Project), nil, &h)
		return map[string]any{"history": h}, err
	case in.Fingerprint != "":
		var f engine.TrackedFinding
		if err := c.apiJSON(ctx, "list_findings", http.MethodGet, apiPath("/api/v1/projects/%s/findings/%s", in.Project, in.Fingerprint), nil, &f); err != nil {
			return out, err
		}
		clipTracked(&f)
		return map[string]any{"finding": f}, nil
	}
	q := url.Values{"status": {in.Status}, "check": {in.Check}, "q": {in.Search}, "severity": {strings.Join(in.Severities, ",")}, "limit": {strconv.Itoa(in.Limit)}, "offset": {strconv.Itoa(in.Offset)}}
	if in.Oldest {
		q.Set("sort", "oldest")
	}
	var l engine.StandingList
	if err := c.apiJSON(ctx, "list_findings", http.MethodGet, apiPath("/api/v1/projects/%s/findings", in.Project)+"?"+q.Encode(), nil, &l); err != nil {
		return out, err
	}
	for i := range l.Findings {
		clipTracked(&l.Findings[i])
	}
	return map[string]any{"findings": l.Findings, "matched": l.Matched, "open": l.Open, "accepted": l.Accepted, "fixed": l.Fixed, "by_severity": l.BySeverity, "by_check": l.ByCheck}, nil
}

type exportFindingsIn struct {
	Project string `json:"project" jsonschema:"the Flight's name"`
	Format  string `json:"format,omitempty" jsonschema:"json (default), csv or sarif"`
	Status  string `json:"status,omitempty" jsonschema:"open (default), fixed, accepted or all"`
}

func exportFindings(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in exportFindingsIn) (map[string]any, error) {
	if in.Project == "" {
		return nil, errors.New("say which Flight")
	}
	if in.Format == "" {
		in.Format = "json"
	}
	text, err := c.apiText(ctx, "export_findings", apiPath("/api/v1/projects/%s/findings/export", in.Project)+"?format="+url.QueryEscape(in.Format)+"&status="+url.QueryEscape(in.Status))
	if err != nil {
		return nil, err
	}
	return map[string]any{"format": in.Format, "text": untrusted(text, 4<<20)}, nil
}

type inspectionIn struct {
	RunID      string   `json:"run_id" jsonschema:"the run's id"`
	Standing   string   `json:"standing,omitempty" jsonschema:"only new, existing, accepted, false-positive or unscored findings"`
	Check      string   `json:"check,omitempty" jsonschema:"only the findings of this check, such as secrets"`
	Severities []string `json:"severities,omitempty" jsonschema:"any of critical, high, medium, low, unknown"`
	Limit      int      `json:"limit,omitempty" jsonschema:"at most this many findings (100 by default, 500 at most)"`
	Offset     int      `json:"offset,omitempty" jsonschema:"skip this many, for the next page"`
}

func getRunInspection(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in inspectionIn) (engine.RunInspection, error) {
	var out engine.RunInspection
	if in.RunID == "" {
		return out, errors.New("say which run")
	}
	q := url.Values{"standing": {in.Standing}, "check": {in.Check}, "severity": {strings.Join(in.Severities, ",")}, "limit": {strconv.Itoa(in.Limit)}, "offset": {strconv.Itoa(in.Offset)}}
	if err := c.apiJSON(ctx, "get_run_inspection", http.MethodGet, apiPath("/api/v1/runs/%s/inspection", in.RunID)+"?"+q.Encode(), nil, &out); err != nil {
		return out, err
	}
	for i := range out.Findings {
		f := &out.Findings[i]
		f.Title, f.File, f.Fix, f.Rule, f.Package = untrusted(f.Title, 500), untrusted(f.File, 300), untrusted(f.Fix, 500), untrusted(f.Rule, 200), untrusted(f.Package, 200)
		if f.Decision != nil {
			f.Decision.Reason = untrusted(f.Decision.Reason, 500)
		}
	}
	out.Summary = untrusted(out.Summary, 1500)
	return out, nil
}

type acceptFindingIn struct {
	Project     string `json:"project" jsonschema:"the Flight's name"`
	Fingerprint string `json:"fingerprint,omitempty" jsonschema:"the finding's fingerprint, from list_findings or get_run_inspection; or give path instead"`
	Path        string `json:"path,omitempty" jsonschema:"instead of one finding: a path of the repository, such as docs/** or docs/vectors.json; every open finding there gets the decision (for example: these are test vectors, not secrets)"`
	Check       string `json:"check,omitempty" jsonschema:"with path: only the findings of this check, such as secrets"`
	Status      string `json:"status,omitempty" jsonschema:"accepted-risk (default) or false-positive"`
	Reason      string `json:"reason" jsonschema:"why it is safe to live with, or why it is not a problem (at least 10 characters); it is recorded with the caller's name"`
	Expires     string `json:"expires" jsonschema:"the date the decision ends, YYYY-MM-DD, at most a year away"`
}

func acceptFinding(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in acceptFindingIn) (engine.DecisionView, error) {
	var out engine.DecisionView
	if in.Project == "" || (in.Fingerprint == "") == (in.Path == "") {
		return out, errors.New("say which Flight and either which finding (fingerprint) or which path")
	}
	if in.Path != "" {
		var n map[string]any
		err := c.apiJSON(ctx, "accept_finding", http.MethodPost, apiPath("/api/v1/projects/%s/findings/accept-path", in.Project),
			map[string]string{"path": in.Path, "check": in.Check, "status": in.Status, "reason": in.Reason, "expires": in.Expires}, &n)
		if v, ok := n["decided"].(float64); ok {
			out.Reason, out.Status = fmt.Sprintf("%d findings in %s were decided", int(v), in.Path), in.Status
		}
		return out, err
	}
	err := c.apiJSON(ctx, "accept_finding", http.MethodPost, apiPath("/api/v1/projects/%s/findings/%s/accept", in.Project, in.Fingerprint),
		map[string]string{"status": in.Status, "reason": in.Reason, "expires": in.Expires}, &out)
	return out, err
}

func reopenFinding(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
	Project     string `json:"project" jsonschema:"the Flight's name"`
	Fingerprint string `json:"fingerprint" jsonschema:"the finding's fingerprint"`
}) (map[string]bool, error) {
	out := map[string]bool{}
	err := c.apiJSON(ctx, "reopen_finding", http.MethodPost, apiPath("/api/v1/projects/%s/findings/%s/reopen", in.Project, in.Fingerprint), map[string]string{}, &out)
	return out, err
}

// apiText calls the API and returns a successful answer as text.
func (c *mcpCall) apiText(ctx context.Context, tool, path string) (string, error) {
	ans, err := c.api(ctx, tool, http.MethodGet, path, nil, 4<<20)
	if err != nil {
		return "", err
	}
	if ans.Status >= 400 {
		return "", answerError(ans)
	}
	if ans.Truncated {
		return "", &apiError{Status: 500, Message: "the answer is larger than 4 MiB; narrow it by status"}
	}
	return string(ans.Body), nil
}
