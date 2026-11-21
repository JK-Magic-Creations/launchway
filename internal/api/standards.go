package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/standards"
	standardsservice "github.com/jk-magic-creations/launchway/internal/standards/service"
)

// StandardsService is Standing orders on a server (internal/standards/service).
type StandardsService = standardsservice.Service

// Standing orders: the same scans and best-practice checks for every
// repository, on by default. The routes serve the profile (what can run and
// why), the settings of each level, a project's effective profile with where
// each value comes from, the backlog of findings and what people decided
// about them, and a run's Inspection. Who may do what follows the project's
// roles, and the level's for settings (see mayChangeLevel).

func (s *Server) standardsRoutes() []route {
	const p = "/api/v1/projects/{name}"
	return []route{
		{"GET /api/v1/standards/profile", signedIn, "", s.standardsProfile},
		{"GET /api/v1/standards/settings", signedIn, "", s.standardsGetSettings},
		{"PUT /api/v1/standards/settings", signedIn, "standards.settings_set", s.standardsSetSettings},
		{"GET /api/v1/standards/coverage", signedIn, "", s.standardsCoverage},
		{"GET " + p + "/standards", projectRead, "", s.projectStandards},
		{"GET " + p + "/standards/file", projectRead, "", s.projectStandardsFile},
		{"GET " + p + "/findings", projectRead, "", s.projectFindings},
		{"GET " + p + "/findings/history", projectRead, "", s.projectFindingsHistory},
		{"GET " + p + "/findings/export", projectRead, "", s.projectFindingsExport},
		{"GET " + p + "/findings/{fingerprint}", projectRead, "", s.projectFinding},
		{"POST " + p + "/findings/{fingerprint}/accept", projectOperate, "finding.accept", s.acceptFinding},
		{"POST " + p + "/findings/{fingerprint}/reopen", projectOperate, "finding.reopen", s.reopenFinding},
		{"GET /api/v1/runs/{id}/inspection", runRead, "", s.runInspection},
	}
}

func (s *Server) standardsOn(w http.ResponseWriter) bool {
	if s.Standards == nil || !s.Engine.StandingsKnown() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Standing orders are not available on this server", "code": "feature_off",
			"next": "Standing orders need a database of schema 45; start a server of this release."})
		return false
	}
	return true
}

// ---- the profile ----

type profileCheck struct {
	standards.Check
	StateDefault string `json:"state_default"`
	License      string `json:"license,omitempty"`
	Image        string `json:"image,omitempty"`
	Budget       int    `json:"budget_seconds"`
	Available    bool   `json:"available"`
}

func (s *Server) standardsProfile(w http.ResponseWriter, r *http.Request) {
	tools, err := standards.Tools()
	if err != nil {
		s.fail(w, err)
		return
	}
	var checks []profileCheck
	for _, c := range standards.Checks() {
		pc := profileCheck{Check: c, StateDefault: string(c.Default), Budget: int(c.Budget / time.Second), Available: c.Available()}
		if t, ok := standards.ToolNamed(c.Tool); ok {
			pc.License, pc.Image = t.License, t.Image
		}
		checks = append(checks, pc)
	}
	ecos := make([]map[string]string, 0)
	for _, e := range standards.Ecosystems {
		ecos = append(ecos, map[string]string{"id": e, "title": standards.EcosystemTitle[e]})
	}
	keys := []string{standards.KeyOff, standards.KeyFailOn, standards.KeyRequireFix, standards.KeyDefaultBranch, standards.KeyIncomplete, standards.KeyAllow, standards.KeyDeny, standards.KeyIgnore}
	for _, c := range standards.CheckIDs() {
		keys = append(keys, standards.CheckKey(c))
	}
	names := map[string]string{}
	for _, k := range keys {
		names[k] = standards.KeyName(k)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": standards.ProfileVersion, "ecosystems": ecos, "checks": checks, "tools": tools, "keys": keys, "key_names": names,
		"defaults": map[string]any{"fail_on": standards.Defaults.FailOn, "require_fix": standards.Defaults.RequireFix, "default_branch": standards.Defaults.DefaultBranch,
			"incomplete": standards.Defaults.Incomplete, "licenses_deny": standards.Defaults.Deny},
		"file": standards.FilePath, "words": s.Standards.Words(),
	})
}

// ---- settings by level ----

func (s *Server) standardsLevel(w http.ResponseWriter, level, scope string) (string, bool) {
	level = strings.ReplaceAll(level, "organisation", "organization")
	switch level {
	case standards.LevelInstallation:
		return "", true
	case standards.LevelOrganization:
		return settings.DefaultOrg, true
	case standards.LevelUnit, standards.LevelProject:
		if scope == "" {
			writeErr(w, http.StatusBadRequest, "say which "+level+": &scope=NAME")
			return "", false
		}
		return scope, true
	}
	writeErr(w, http.StatusBadRequest, "level is installation, organization, unit or project")
	return "", false
}

func (s *Server) standardsGetSettings(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	q := r.URL.Query()
	level := strings.ReplaceAll(q.Get("level"), "organisation", "organization")
	scope, ok := s.standardsLevel(w, level, q.Get("scope"))
	if !ok {
		return
	}
	if code, msg := s.mayReadLevel(identityFrom(r), settings.Level(level), scope); code != 0 {
		writeErr(w, code, msg)
		return
	}
	set, version, err := s.Standards.Doc(level, scope)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"level": level, "scope": scope, "settings": set, "version": version, "words": s.Standards.Words()})
}

type standardsSettingsBody struct {
	Level    string             `json:"level"`
	Scope    string             `json:"scope"`
	Settings standards.Settings `json:"settings"`
	Version  int                `json:"version"`
}

func (s *Server) standardsSetSettings(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	var in standardsSettingsBody
	if !decode(w, r, &in) {
		return
	}
	in.Level = strings.ReplaceAll(in.Level, "organisation", "organization")
	scope, ok := s.standardsLevel(w, in.Level, in.Scope)
	if !ok {
		return
	}
	if code, msg := s.mayChangeLevel(identityFrom(r), settings.Level(in.Level), scope, true); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if ps := in.Settings.Problems(time.Now(), true); len(ps) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "these settings cannot be saved: " + strings.Join(ps, " "), "code": "invalid", "problems": ps})
		return
	}
	// A level may not weaken what a level above locked; the page says so
	// plainly instead of saving something that would be ignored.
	if refused, err := s.standardsWeakens(in.Level, scope, in.Settings); err != nil {
		s.fail(w, err)
		return
	} else if len(refused) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": strings.Join(refused, " "), "code": "locked", "problems": refused,
			"next": "Ask the administrator who set the lock, or choose a value that is not weaker."})
		return
	}
	raw, _ := json.Marshal(in.Settings)
	before, after, err := s.Engine.SetStandingSettings(in.Level, scope, raw, in.Version, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	detail := "settings of " + in.Level + " " + scope + " saved (version " + strconv.Itoa(after.Version) + ")"
	if in.Settings.Off != nil && *in.Settings.Off {
		detail = "STANDING ORDERS SWITCHED OFF for " + in.Level + " " + scope + ": " + in.Settings.OffReason
	} else if before != nil {
		var old standards.Settings
		if json.Unmarshal(before.Doc, &old) == nil && old.Off != nil && *old.Off && (in.Settings.Off == nil || !*in.Settings.Off) {
			detail = "standing orders switched back on for " + in.Level + " " + scope
		}
	}
	setAudit(r, in.Level+"/"+scope, detail)
	writeJSON(w, http.StatusOK, map[string]any{"level": in.Level, "scope": scope, "settings": in.Settings, "version": after.Version})
}

// standardsWeakens says what, in a level's new settings, a level above has
// locked against.
func (s *Server) standardsWeakens(level, scope string, set standards.Settings) ([]string, error) {
	var above []standards.LevelDoc
	add := func(l, sc string) error {
		d, _, err := s.Standards.Doc(l, sc)
		if err != nil {
			return err
		}
		above = append(above, standards.LevelDoc{Level: l, Scope: sc, Settings: d})
		return nil
	}
	switch level {
	case standards.LevelInstallation:
		return nil, nil
	case standards.LevelOrganization:
		if err := add(standards.LevelInstallation, ""); err != nil {
			return nil, err
		}
	case standards.LevelUnit:
		if err := add(standards.LevelInstallation, ""); err != nil {
			return nil, err
		}
		if err := add(standards.LevelOrganization, settings.DefaultOrg); err != nil {
			return nil, err
		}
	case standards.LevelProject:
		lv, err := s.Standards.Levels(scope)
		if err != nil {
			return nil, err
		}
		for _, l := range lv {
			if l.Level != standards.LevelProject {
				above = append(above, l)
			}
		}
	}
	// The setting that is being saved is the nearest layer.
	above = append(above, standards.LevelDoc{Level: level, Scope: scope, Settings: set})
	eff := standards.Resolve(standards.ResolveInput{Levels: above, Now: time.Now().UTC(), Words: s.Standards.Words()})
	var out []string
	for _, n := range eff.Notices {
		if strings.Contains(n, "locked") {
			out = append(out, n)
		}
	}
	return out, nil
}

// ---- a project ----

func (s *Server) projectProfile(r *http.Request) (*standardsservice.Project, error) {
	name := r.PathValue("name")
	dir, branch, ok := s.Projects.ProjectGit(name)
	if !ok {
		return nil, fmt.Errorf("%w: project %s", engine.ErrNotFound, name)
	}
	return s.Standards.ForProject(r.Context(), name, dir, branch)
}

func (s *Server) projectStandards(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	p, err := s.projectProfile(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// projectStandardsFile answers what the repository's file would hold if it
// were written from the project's own settings, so a team can move them to
// Git; and what is in the repository now.
func (s *Server) projectStandardsFile(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	name := r.PathValue("name")
	set, _, err := s.Standards.Doc(standards.LevelProject, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	set.Locks = nil
	p, err := s.projectProfile(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": standards.FilePath, "in_repository": p.File, "repository_text": p.FileText, "problems": p.FileProblems,
		"from_settings": string(standards.ExportFile(set))})
}

// ---- findings ----

func (s *Server) projectFindings(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	q := r.URL.Query()
	lim, _ := strconv.Atoi(q.Get("limit"))
	off, _ := strconv.Atoi(q.Get("offset"))
	var sevs []string
	if v := q.Get("severity"); v != "" {
		sevs = strings.Split(v, ",")
	}
	out, err := s.Engine.StandingFindings(r.PathValue("name"), engine.StandingQuery{Status: q.Get("status"), Severities: sevs, Check: q.Get("check"), Search: q.Get("q"),
		Oldest: q.Get("sort") == "oldest", Limit: lim, Offset: off})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectFindingsHistory(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	out, err := s.Engine.StandingHistory(r.PathValue("name"), days)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) projectFinding(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	list, err := s.Engine.StandingFindings(r.PathValue("name"), engine.StandingQuery{Status: "all", Limit: 500, Search: ""})
	if err != nil {
		s.fail(w, err)
		return
	}
	fp := r.PathValue("fingerprint")
	for _, f := range list.Findings {
		if f.Fingerprint == fp {
			writeJSON(w, http.StatusOK, f)
			return
		}
	}
	// Past the first page, or not on the default branch: look through all.
	all, err := s.Engine.StandingFindings(r.PathValue("name"), engine.StandingQuery{Status: "all", Limit: 500, Offset: 500})
	if err == nil {
		for _, f := range all.Findings {
			if f.Fingerprint == fp {
				writeJSON(w, http.StatusOK, f)
				return
			}
		}
	}
	s.fail(w, fmt.Errorf("%w: finding %s of project %s (only what the default branch has had is listed; a finding of a pull request is in its run's Inspection)", engine.ErrNotFound, fp, r.PathValue("name")))
}

type acceptBody struct {
	Status  string `json:"status"` // accepted-risk (default) or false-positive
	Reason  string `json:"reason"`
	Expires string `json:"expires"` // YYYY-MM-DD, or "in 30 days" style is for the clients to turn into a date
}

func (s *Server) acceptFinding(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	var in acceptBody
	if !decode(w, r, &in) {
		return
	}
	if in.Status == "" {
		in.Status = standards.StatusAccepted
	}
	exp, err := time.Parse(standards.DateFormat, in.Expires)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "say when the decision ends: expires as YYYY-MM-DD, at most "+strconv.Itoa(standards.MaxExemptionDays)+" days from today")
		return
	}
	exp = exp.Add(24*time.Hour - time.Second)
	fp := r.PathValue("fingerprint")
	d, err := s.Engine.DecideFinding(r.PathValue("name"), fp, in.Status, in.Reason, identityFrom(r).User, exp)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, fp, in.Status+" until "+in.Expires+": "+d.Reason)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) reopenFinding(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	fp := r.PathValue("fingerprint")
	if err := s.Engine.ReopenFinding(r.PathValue("name"), fp, identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, fp, "decision ended")
	writeJSON(w, http.StatusOK, map[string]bool{"reopened": true})
}

func (s *Server) projectFindingsExport(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	q := r.URL.Query()
	list, err := s.Engine.StandingFindings(r.PathValue("name"), engine.StandingQuery{Status: q.Get("status"), Limit: 500})
	if err != nil {
		s.fail(w, err)
		return
	}
	var all []engine.TrackedFinding
	all = append(all, list.Findings...)
	for off := 500; off < list.Matched; off += 500 {
		page, err := s.Engine.StandingFindings(r.PathValue("name"), engine.StandingQuery{Status: q.Get("status"), Limit: 500, Offset: off})
		if err != nil {
			s.fail(w, err)
			return
		}
		all = append(all, page.Findings...)
	}
	fs := make([]standards.Finding, 0, len(all))
	for _, t := range all {
		fs = append(fs, standards.Finding{Check: t.Check, Tool: t.Tool, Rule: t.Rule, Severity: severityOf(t.Severity), Kind: standards.Kind(t.Kind), Title: t.Title, File: t.File,
			Line: t.Line, Package: t.Package, Version: t.Version, FixedIn: t.FixedIn, Advisories: t.Advisories, Reachable: t.Reachable, Fingerprint: t.Fingerprint})
	}
	name := r.PathValue("name")
	switch f := q.Get("format"); f {
	case "", "json":
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-findings.json"`)
		writeJSON(w, http.StatusOK, map[string]any{"project": name, "findings": all})
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-findings.csv"`)
		_ = standards.ExportCSV(w, fs)
	case "sarif":
		w.Header().Set("Content-Type", "application/sarif+json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-findings.sarif"`)
		_ = standards.ExportSARIF(w, fs)
	default:
		writeErr(w, http.StatusBadRequest, "format is json, csv or sarif")
	}
}

func (s *Server) runInspection(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	q := r.URL.Query()
	lim, _ := strconv.Atoi(q.Get("limit"))
	off, _ := strconv.Atoi(q.Get("offset"))
	var sevs []string
	if v := q.Get("severity"); v != "" {
		sevs = strings.Split(v, ",")
	}
	out, err := s.Engine.RunInspection(r.PathValue("id"), engine.InspectionQuery{Standing: q.Get("standing"), Check: q.Get("check"), Severities: sevs, Limit: lim, Offset: off})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- coverage ----

type coverageRow struct {
	Project string                  `json:"project"`
	Unit    string                  `json:"unit,omitempty"`
	Off     bool                    `json:"off"`
	OffBy   string                  `json:"off_by,omitempty"`
	Reason  string                  `json:"off_reason,omitempty"`
	Empty   bool                    `json:"empty,omitempty"`
	Checks  map[string]coverageCell `json:"checks"`
	Open    map[string]int          `json:"open"`
	Notices int                     `json:"notices"`
}

type coverageCell struct {
	State  string `json:"state"`
	Active bool   `json:"active"`
	Locked bool   `json:"locked,omitempty"`
}

func (s *Server) standardsCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.standardsOn(w) {
		return
	}
	q := r.URL.Query()
	level := strings.ReplaceAll(q.Get("level"), "organisation", "organization")
	if level == "" {
		level = standards.LevelInstallation
	}
	scope, ok := s.standardsLevel(w, level, q.Get("scope"))
	if !ok {
		return
	}
	id := identityFrom(r)
	var names []string
	switch level {
	case standards.LevelProject:
		names = []string{scope}
	case standards.LevelUnit:
		names = s.unitProjects(scope)
	default:
		for _, p := range s.Projects.List() {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	rows := []coverageRow{}
	var offAny bool
	for _, name := range names {
		if !s.authorize(id, auth.Read, name) {
			continue
		}
		dir, branch, ok := s.Projects.ProjectGit(name)
		if !ok {
			continue
		}
		p, err := s.Standards.ForProject(r.Context(), name, dir, branch)
		if err != nil {
			continue
		}
		unit, _ := s.Standards.ScopeOf(name)
		row := coverageRow{Project: name, Unit: unit, Off: p.Effective.Off.V, Empty: p.Empty, Checks: map[string]coverageCell{}, Open: map[string]int{}, Notices: len(p.Effective.Notices)}
		if row.Off {
			offAny = true
			row.OffBy, row.Reason = p.Effective.Off.From.Level, p.Effective.OffReason
		}
		for _, c := range p.Effective.Checks {
			if c.Applies || c.Active {
				row.Checks[c.ID] = coverageCell{State: string(c.State.V), Active: c.Active, Locked: c.State.Locked != nil}
			}
		}
		if h, err := s.Engine.StandingHistory(name, 1); err == nil {
			row.Open = h.BySeverity
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"level": level, "scope": scope, "projects": rows, "any_off": offAny, "words": s.Standards.Words()})
}

func severityOf(s string) findings.Severity { return findings.Severity(s) }
