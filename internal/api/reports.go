package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Schema 18 in the API: the comparison of a run's tests with the previous
// run, test attachments, coverage, and hosted HTML reports.
//
// Everything a test report, an attachment or an HTML report holds was
// written by whoever could push code, pull-request authors included, so it
// is served as hostile:
//
//   - An attachment is served inline only when its content (not its name or
//     declared type) is a PNG, JPEG, GIF or WebP image, a WebM or MP4
//     video, or plain text, and then with that type, nosniff and a
//     Content-Security-Policy that allows nothing and sandboxes it. Anything
//     else (HTML, SVG, XML, PDF, archives such as Playwright traces) is a
//     download, application/octet-stream, with the same headers.
//   - HTML reports are served under /hosted/<link>/, where <link> is a
//     signed, expiring capability for one report, never from a path that
//     takes the viewer's session. Each response sandboxes the page (a
//     Content-Security-Policy sandbox without allow-same-origin: the page's
//     origin is opaque, so it has no access to Launchway's cookies or
//     storage, and the browser treats its requests as cross-site, so the
//     session cookie, SameSite=Lax, is not sent), and limits everything it
//     may load or connect to to the report's own /hosted/<link>/ path. The
//     UI also frames it with the iframe sandbox attribute. Cookie-authenticated
//     API requests that come from an opaque origin (Origin: null) or another
//     site are refused (see guard), and anything that changes something
//     needs the CSRF header, which a sandboxed page cannot send without a
//     CORS preflight this server never grants. With reports.origin set, the
//     reports are served only from that separate origin, which holds no
//     Launchway session at all.

// runTestCompare is a run's tests beside the previous run of the same ref:
// new failures, fixed, still failing.
func (s *Server) runTestCompare(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.CompareTests(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// testAttachment serves one attachment of a test result, with ranges (so
// videos can be played and sought).
func (s *Server) testAttachment(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	caseID, err := strconv.ParseInt(r.PathValue("case"), 10, 64)
	n, err2 := strconv.Atoi(r.PathValue("n"))
	if err != nil || err2 != nil || caseID <= 0 || n < 0 {
		s.fail(w, fmt.Errorf("%w: attachment", engine.ErrNotFound))
		return
	}
	f, err := s.Engine.OpenTestAttachment(r.Context(), runID, caseID, n, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The type is decided from the first bytes of the content.
	head := make([]byte, 512)
	k, _ := io.ReadFull(f.Body, head)
	f.Body.Close()
	sniffed := engine.SniffAttachment(head[:k])
	if strings.HasPrefix(sniffed, "text/") && f.Kind != "text" {
		// Markup (SVG, HTML) can look like text; only what the report
		// says is text is shown as text.
		sniffed = "application/octet-stream"
	}
	name := f.Name
	if name == "" {
		name = path.Base(f.Path)
	}
	h := w.Header()
	inline := r.URL.Query().Get("download") == "" && sniffed != "application/octet-stream"
	if inline {
		h.Set("Content-Type", sniffed)
		h.Set("Content-Disposition", strings.Replace(attachment(name), "attachment;", "inline;", 1))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		h.Set("Cache-Control", "private, max-age=3600")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
	} else {
		downloadHeaders(h, name)
	}
	h.Set("ETag", `"sha256-`+f.SHA256+`"`)
	rs := &artifactSeeker{ctx: r.Context(), size: f.Size, open: func(ctx context.Context, off int64) (io.ReadCloser, error) {
		g, err := s.Engine.OpenTestAttachment(ctx, runID, caseID, n, off)
		if err != nil {
			return nil, err
		}
		return g.Body, nil
	}}
	defer rs.Close()
	http.ServeContent(w, r, "", time.Time{}, rs)
}

// runCoverage is a run's coverage, step by step, with the previous run of
// its ref and the project's default branch to compare.
func (s *Server) runCoverage(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	project, err := s.Engine.RunProject(runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	def := ""
	if s.Projects != nil {
		if p, err := s.Projects.Get(project); err == nil && p.DefaultBranch != "" {
			def = "refs/heads/" + p.DefaultBranch
		}
	}
	out, err := s.Engine.RunCoverage(runID, def)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// runCoverageFiles lists a run's per-file coverage: ?step=, ?q=,
// ?sort=path|lines|missed, ?limit=, ?offset=.
func (s *Server) runCoverageFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cq := engine.CoverageFileQuery{Step: q.Get("step"), Search: q.Get("q"), Sort: q.Get("sort")}
	var err error
	if cq.Limit, err = intParam(r, "limit"); err != nil {
		s.fail(w, err)
		return
	}
	if cq.Offset, err = intParam(r, "offset"); err != nil {
		s.fail(w, err)
		return
	}
	if len(cq.Search) > 200 {
		writeErr(w, http.StatusBadRequest, "the search text is longer than 200 bytes")
		return
	}
	out, err := s.Engine.RunCoverageFiles(r.PathValue("id"), cq)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// projectCoverage is a project's coverage over the last runs of a ref
// (?ref=, ?runs=).
func (s *Server) projectCoverage(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "runs")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.ProjectCoverage(r.PathValue("name"), s.projectRef(r), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- hosted HTML reports ----

// hostedLinkTTL is how long a link to an HTML report works. The UI asks
// for new links when it shows the reports.
const hostedLinkTTL = 4 * time.Hour

// HostedReport is an HTML report as the API lists it: with a link that
// opens it.
type HostedReport struct {
	engine.HTMLReport
	URL string `json:"url,omitempty"` // to its index page; absent when the index is missing
	// Base is the link to the report's directory, for other pages.
	Base string `json:"base"`
}

// runReports lists a run's HTML reports, each with a fresh link.
func (s *Server) runReports(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.RunHTMLReports(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	key, err := s.hostedKey()
	if err != nil {
		s.fail(w, err)
		return
	}
	out := struct {
		Reports  []HostedReport `json:"reports"`
		Declared []string       `json:"declared"`
		// Origin says where the reports are served from: "separate" (a
		// reports origin is configured) or "sandboxed" (Launchway's own
		// origin, sandboxed).
		Origin string `json:"origin"`
	}{Reports: []HostedReport{}, Declared: list.Declared, Origin: "sandboxed"}
	if s.ReportsOrigin != "" {
		out.Origin = "separate"
	}
	exp := s.Now().Add(hostedLinkTTL).Unix()
	for _, h := range list.Reports {
		base := s.ReportsOrigin + "/hosted/" + signHosted(key, h.Artifact, exp) + "/"
		hr := HostedReport{HTMLReport: h, Base: base}
		if h.HasIndex {
			hr.URL = base + escapePath(h.Index)
		}
		out.Reports = append(out.Reports, hr)
	}
	writeJSON(w, http.StatusOK, out)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// hostedKey is the key that signs links to HTML reports, shared by every
// server.
func (s *Server) hostedKey() ([]byte, error) {
	s.sealerMu.Lock()
	defer s.sealerMu.Unlock()
	if s.reportKey != nil {
		return s.reportKey, nil
	}
	v, err := s.Engine.SharedValue("hosted_report_key", auth.NewKey())
	if err != nil {
		return nil, err
	}
	s.reportKey = []byte(v)
	return s.reportKey, nil
}

// signHosted makes the link part for one report: its artifact ID, when the
// link stops working, and a MAC of both.
func signHosted(key []byte, artifactID string, exp int64) string {
	payload := artifactID + "." + strconv.FormatInt(exp, 36)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("launchway hosted report v1\x00" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:24])
}

// checkHosted reads a link part, returning the artifact ID if the MAC is
// right and the link has not expired.
func checkHosted(key []byte, link string, now time.Time) (string, bool) {
	parts := strings.Split(link, ".")
	if len(parts) != 3 || len(link) > 200 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil {
		return "", false
	}
	want := signHosted(key, parts[0], exp)
	if !hmac.Equal([]byte(want), []byte(link)) || now.Unix() > exp {
		return "", false
	}
	return parts[0], true
}

// hosted serves a file of an HTML report: GET /hosted/{link}/{path...}.
// It takes no session: the link is the permission, scoped to one report
// for a few hours, handed out only to people who may see the run.
func (s *Server) hosted(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=600")
	h.Set("Cross-Origin-Resource-Policy", "same-site")
	if s.ReportsOrigin != "" && !sameHost(s.ReportsOrigin, r.Host) {
		// Reports run script; with a separate origin configured, they
		// never run on Launchway's own.
		writeErr(w, http.StatusNotFound, "HTML reports are served from "+s.ReportsOrigin)
		return
	}
	key, err := s.hostedKey()
	if err != nil {
		s.fail(w, err)
		return
	}
	link := r.PathValue("link")
	artifactID, ok := checkHosted(key, link, s.Now())
	if !ok {
		hostedError(w, http.StatusNotFound, "This link to a report has expired or is not valid. Open the report again from its run in Launchway.")
		return
	}
	page := r.PathValue("path")
	if page == "" || strings.HasSuffix(page, "/") {
		page += "index.html"
	}
	f, err := s.Engine.OpenHostedFile(r.Context(), artifactID, page, 0)
	if err != nil {
		hostedError(w, http.StatusNotFound, "Not found in this report.")
		return
	}
	f.Body.Close()
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	base := scheme + "://" + r.Host + "/hosted/" + link + "/"
	ancestors := "'self'"
	if s.ReportsOrigin != "" {
		ancestors = "*" // framed by Launchway's own origin, which is another one
	}
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", hostedCSP(base, ancestors))
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(page)))
	switch {
	case ct == "":
		ct = "application/octet-stream"
	case strings.HasPrefix(ct, "text/"), ct == "application/javascript", ct == "application/json", ct == "image/svg+xml":
		if !strings.Contains(ct, "charset") {
			ct += "; charset=utf-8"
		}
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", `"sha256-`+f.SHA256+`"`)
	rs := &artifactSeeker{ctx: r.Context(), size: f.Size, open: func(ctx context.Context, off int64) (io.ReadCloser, error) {
		g, err := s.Engine.OpenHostedFile(ctx, artifactID, page, off)
		if err != nil {
			return nil, err
		}
		return g.Body, nil
	}}
	defer rs.Close()
	http.ServeContent(w, r, "", time.Time{}, rs)
}

// hostedCSP is the policy every file of an HTML report is served with: a
// sandbox without allow-same-origin, and nothing loaded from, sent to or
// framed from anywhere but the report's own directory.
func hostedCSP(base, ancestors string) string {
	src := base
	return "sandbox allow-scripts allow-popups allow-modals allow-downloads; " +
		"default-src " + src + "; " +
		"script-src " + src + " 'unsafe-inline' 'unsafe-eval'; " +
		"style-src " + src + " 'unsafe-inline'; " +
		"img-src " + src + " data: blob:; " +
		"media-src " + src + " data: blob:; " +
		"font-src " + src + " data:; " +
		"connect-src " + src + " data: blob:; " +
		"frame-src " + src + " data: blob:; " +
		"worker-src 'none'; object-src 'none'; form-action 'none'; base-uri 'none'; " +
		"frame-ancestors " + ancestors
}

func hostedError(w http.ResponseWriter, code int, msg string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, msg+"\n")
}

func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}

// splitOrigin keeps everything but hosted reports off the reports origin,
// when one is configured.
func (s *Server) splitOrigin(next http.Handler) http.Handler {
	if s.ReportsOrigin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sameHost(s.ReportsOrigin, r.Host) && !strings.HasPrefix(r.URL.Path, "/hosted/") && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			hostedError(w, http.StatusNotFound, "This address serves only HTML reports.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromSandboxOrOtherSite reports whether a request comes from a page of
// another site or from a sandboxed page (an HTML report), which must not act
// with the viewer's session cookie. Top-level navigations (a link someone
// followed) are allowed; the browser sends Lax cookies only with those.
func fromSandboxOrOtherSite(r *http.Request) bool {
	if r.Header.Get("Origin") == "null" {
		return true
	}
	return r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.Header.Get("Sec-Fetch-Mode") != "navigate"
}
