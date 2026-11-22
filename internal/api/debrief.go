package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/debrief"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// The Debrief: the report of a run's tests, its exports (one HTML file, a
// PDF, JSON, merged JUnit), a project's flaky tests, per-test timings and
// the split of tests among shards. Whoever may read a run may read and
// export its Debrief; a project's pages need the project to be readable.

// DebriefSettings are the installation's choices for how Debriefs look.
type DebriefSettings struct {
	// Name and Logo brand the exports; the defaults are neutral (the
	// product's name and no logo). Logo is a PNG or JPEG data URL.
	Name string
	Logo string
	// ChangeFloor is the share of a change's lines that should be covered
	// (advice only); 0 is engine.DefaultChangeFloor.
	ChangeFloor float64
}

var debriefSettings sync.Map // *Server → DebriefSettings

// SetDebrief sets how this server's exported Debriefs are branded and
// judged; the zero value is the neutral default.
func (s *Server) SetDebrief(d DebriefSettings) { debriefSettings.Store(s, d) }

func (s *Server) debriefSet() DebriefSettings {
	if v, ok := debriefSettings.Load(s); ok {
		return v.(DebriefSettings)
	}
	return DebriefSettings{}
}

// requestBase is the address the request came to, for links in a report.
func (s *Server) requestBase(r *http.Request) string {
	scheme := "http"
	host := r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if s.proxyTrusted(r) {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" && !strings.ContainsAny(h, " /\\,") {
			host = h
		}
	}
	return scheme + "://" + host
}

// debriefFor builds the Debrief of the run in {id}.
func (s *Server) debriefFor(ctx context.Context, r *http.Request, embed bool) (*debrief.Report, error) {
	id := r.PathValue("id")
	run, err := s.Engine.GetRun(id)
	if err != nil {
		return nil, err
	}
	set := s.debriefSet()
	o := engine.DebriefOptions{EmbedImages: embed, Brand: debrief.Brand{Name: set.Name, Logo: set.Logo, URL: s.requestBase(r)}}
	if s.Projects != nil {
		if p, err := s.Projects.Get(run.Project); err == nil && p.DefaultBranch != "" {
			o.DefaultRef = "refs/heads/" + p.DefaultBranch
		}
	}
	if run.PullRequest > 0 {
		o.Change = s.changedLines(ctx, run)
		if o.Change != nil {
			o.Change.Floor = set.ChangeFloor
		}
	}
	return s.Engine.Debrief(id, o)
}

// changedLines reads the lines a pull request's run changed from the
// repository this server hosts; nil when it does not host it or the run is
// not of the pull request's current head.
func (s *Server) changedLines(ctx context.Context, run *engine.RunDetail) *engine.ChangedLines {
	if s.Forge == nil || s.Forge.Pulls == nil || s.Forge.Provider == nil || s.Projects == nil {
		return nil
	}
	proj, err := s.Projects.Get(run.Project)
	if err != nil {
		return nil
	}
	cursor := ""
	for page := 0; page < 20; page++ {
		pg, err := s.Forge.Provider.Repositories().List(ctx, cursor)
		if err != nil {
			return nil
		}
		for _, repo := range pg.Items {
			if !addressIsRepo(proj.URL, repo.Path) {
				continue
			}
			p, err := s.Forge.Pulls.Store.Get(ctx, repo.ID, run.PullRequest)
			if err != nil || p.HeadSHA != run.SHA {
				return nil
			}
			from := p.MergeBase
			if from == "" {
				from = p.BaseSHA
			}
			c, err := s.Forge.Pulls.Compare(ctx, repo.ID, from, p.HeadSHA, pulls.DefaultLimits)
			if err != nil {
				return nil
			}
			out := &engine.ChangedLines{BaseRef: "refs/heads/" + p.TargetBranch, Files: map[string]string{}}
			for i, f := range c.Files {
				if i >= 300 {
					break
				}
				if f.Binary || f.Status == "deleted" || f.TooLarge {
					continue
				}
				patch, err := s.Forge.Pulls.PatchOf(ctx, repo.ID, from, p.HeadSHA, []string{f.Path}, false, pulls.DefaultLimits)
				if err != nil || patch.Held {
					continue
				}
				if r := debrief.PatchLines(patch.Diff); len(r) > 0 {
					out.Files[f.Path] = r.String()
				}
			}
			return out
		}
		if pg.Next == "" {
			break
		}
		cursor = pg.Next
	}
	return nil
}

// runDebrief is the Debrief of a run as JSON.
func (s *Server) runDebrief(w http.ResponseWriter, r *http.Request) {
	rep, err := s.debriefFor(r.Context(), r, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// DebriefFormats are the forms a Debrief is exported in.
var DebriefFormats = []string{"html", "pdf", "json", "junit"}

// runDebriefExport downloads a run's Debrief: ?format=html|pdf|json|junit.
func (s *Server) runDebriefExport(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "html"
	}
	var ctype, ext string
	switch format {
	case "html":
		ctype, ext = "text/html; charset=utf-8", "html"
	case "pdf":
		ctype, ext = "application/pdf", "pdf"
	case "json":
		ctype, ext = "application/json", "json"
	case "junit":
		ctype, ext = "application/xml", "xml"
	default:
		writeErr(w, http.StatusBadRequest, "format is one of html, pdf, json or junit")
		return
	}
	rep, err := s.debriefFor(r.Context(), r, format == "html" || format == "pdf")
	if err != nil {
		s.fail(w, err)
		return
	}
	var body []byte
	switch format {
	case "html":
		body = debrief.HTML(rep)
	case "pdf":
		if body, err = debrief.PDF(rep); err != nil {
			writeErr(w, http.StatusInternalServerError, "the PDF could not be made: "+err.Error())
			return
		}
	case "json":
		if body, err = json.MarshalIndent(withErrorShape(http.StatusOK, rep), "", "  "); err != nil {
			writeErr(w, http.StatusInternalServerError, "the JSON could not be made: "+err.Error())
			return
		}
		body = append(body, '\n')
	case "junit":
		cases, err := s.Engine.DebriefCases(r.PathValue("id"))
		if err != nil {
			s.fail(w, err)
			return
		}
		buf := &bytes.Buffer{}
		if err := debrief.JUnit(buf, rep, cases); err != nil {
			writeErr(w, http.StatusInternalServerError, "the JUnit file could not be made: "+err.Error())
			return
		}
		body = buf.Bytes()
	}
	name := fmt.Sprintf("debrief-%s-%d.%s", safeName(rep.Project), rep.Run.Number, ext)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(body) //nolint:gosec // G705: a download with nosniff, a sandboxing CSP and attachment disposition; the HTML escapes every report value
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, s)
}

// runDebriefSummary is the short form for a pull request comment or a
// notice: ?format=markdown (the default) or text.
func (s *Server) runDebriefSummary(w http.ResponseWriter, r *http.Request) {
	rep, err := s.debriefFor(r.Context(), r, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	md := debrief.Markdown(rep)
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"markdown": md, "headline": debrief.Headline(rep), "annotations": debrief.Annotations(rep)})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(md)) //nolint:gosec // G705: text/markdown with nosniff, never rendered as HTML by the browser; the text is escaped Markdown of report data
}

// projectFlaky lists a project's flaky tests.
func (s *Server) projectFlaky(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "limit")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.ProjectFlaky(r.PathValue("name"), nil, n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// projectTimings lists a project's tests with their typical durations.
func (s *Server) projectTimings(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "limit")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.TestTimings(r.PathValue("name"), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// projectShards splits a project's tests among ?count= shards by duration.
func (s *Server) projectShards(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "count")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.SplitTests(r.PathValue("name"), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
