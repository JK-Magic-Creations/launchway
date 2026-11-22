package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// Status badges: a small SVG a README can show, for the last Sortie of a Flight on a branch, and for the
// Flight's latest release.
//
//	GET /public/badges/{name}/status.svg?branch=main     passing, failing, running, queued, canceled or no Sorties
//	GET /public/badges/{name}/release.svg                the latest release's version, or none
//	GET /api/v1/projects/{name}/badges                   whether they are on, where they are, what to paste
//	PUT /api/v1/projects/{name}/badges                   switch them on or off (an administrator of the Flight)
//
// A badge is served without signing in ONLY when the Flight's repository is public or the Flight has its badges
// switched on, which is off by default. In every other case, and for a Flight that does not exist, the answer is
// the same plain not found, so the badge says nothing about whether a private Flight is there. A badge holds only
// a status word or a version: never a commit, a message, a person or a number of a run.

var badgeBuckets = &bucket{last: map[string]time.Time{}, n: map[string]float64{}}

func (s *Server) badgeRoutes() []route {
	const p = "/api/v1/projects/{name}/badges"
	return []route{
		{"GET " + p, projectRead, "", s.getBadges},
		{"PUT " + p, projectOperate, "flight.badges", s.putBadges},
		{"GET /public/badges/{name}/status.svg", public, "", s.statusBadge},
		{"GET /public/badges/{name}/release.svg", public, "", s.releaseBadge},
	}
}

// badgesShown says whether a Flight's badges may be seen by anyone, and why: its repository is public here, or its
// badges are switched on.
func (s *Server) badgesShown(r *http.Request, project string) (shown bool, publicRepo bool) {
	if s.Projects != nil && s.Forge != nil {
		if repo, ok := s.hostedRepoOfFlight(r.Context(), project); ok && repo.Visibility == forge.Public {
			publicRepo = true
		}
	}
	on, _ := s.Engine.FlightBadges(project)
	return publicRepo || on, publicRepo
}

type badgeView struct {
	Enabled bool `json:"enabled"`
	// PublicRepository says the Flight's repository is public, so its badges are shown whatever Enabled says.
	PublicRepository bool `json:"public_repository"`
	// Shown says anyone may see the badges now.
	Shown    bool              `json:"shown"`
	Branch   string            `json:"branch"`
	Links    map[string]string `json:"links"`
	Markdown map[string]string `json:"markdown"`
}

func (s *Server) badgeView(r *http.Request, project string) badgeView {
	on, _ := s.Engine.FlightBadges(project)
	shown, pub := s.badgesShown(r, project)
	base := s.baseURL(r) + "/public/badges/" + url.PathEscape(project)
	branch := "main"
	v := badgeView{Enabled: on, PublicRepository: pub, Shown: shown, Branch: branch, Links: map[string]string{
		"status": base + "/status.svg?branch=" + url.QueryEscape(branch), "release": base + "/release.svg"}}
	v.Markdown = map[string]string{
		"status":  "![" + project + " status](" + v.Links["status"] + ")",
		"release": "![" + project + " release](" + v.Links["release"] + ")",
	}
	return v
}

func (s *Server) getBadges(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.badgeView(r, r.PathValue("name")))
}

func (s *Server) putBadges(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	if !s.authorize(identityFrom(r), auth.AdministerProject, project) {
		writeErr(w, http.StatusForbidden, "only an administrator of "+project+" may show or hide its badges")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Engine.SetFlightBadges(project, in.Enabled, identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, project, fmt.Sprintf("badges %s", map[bool]string{true: "on", false: "off"}[in.Enabled]))
	writeJSON(w, http.StatusOK, s.badgeView(r, project))
}

var branchNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,199}$`)

func (s *Server) statusBadge(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	if !s.badgeGate(w, r, project) {
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		branch = "main"
	}
	if !branchNameRe.MatchString(branch) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	value, color := "no Sorties", "#6b7280"
	run, err := s.Engine.LastRunOn(project, branch)
	switch {
	case errors.Is(err, engine.ErrNotFound):
	case err != nil:
		s.fail(w, err)
		return
	default:
		switch run.Status {
		case engine.RunSucceeded:
			value, color = "passing", "#15803d"
		case engine.RunFailed:
			value, color = "failing", "#b91c1c"
		case engine.RunRunning:
			value, color = "running", "#1d4ed8"
		case engine.RunQueued:
			value, color = "waiting", "#6b7280"
		case engine.RunCanceled:
			value, color = "canceled", "#a16207"
		}
	}
	writeBadge(w, r, "build", value, color)
}

func (s *Server) releaseBadge(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	if !s.badgeGate(w, r, project) {
		return
	}
	value, color := "none", "#6b7280"
	if rel, err := s.Engine.LatestRelease(project); err == nil {
		value, color = rel.Version, "#1d4ed8"
	} else if !errors.Is(err, engine.ErrNotFound) {
		s.fail(w, err)
		return
	}
	writeBadge(w, r, "release", value, color)
}

// badgeGate answers the one plain not found for a Flight whose badges are not shown, and limits what one address
// may ask for.
func (s *Server) badgeGate(w http.ResponseWriter, r *http.Request, project string) bool {
	if shown, _ := s.badgesShown(r, project); !shown {
		writeErr(w, http.StatusNotFound, "not found")
		return false
	}
	return s.limitRead(w, r, badgeBuckets, 120, 240, "badges")
}

// writeBadge draws a flat two-part badge. Both texts are escaped; the widths are an estimate of the text's width,
// which is enough for a badge.
func writeBadge(w http.ResponseWriter, r *http.Request, label, value, color string) {
	lw, vw := 14+7*len(label), 14+7*len(value)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, lw+vw, html.EscapeString(label), html.EscapeString(value))
	fmt.Fprintf(&b, `<title>%s: %s</title><rect width="%d" height="20" rx="3" fill="#374151"/><rect x="%d" width="%d" height="20" rx="3" fill="%s"/>`, html.EscapeString(label), html.EscapeString(value), lw, lw, vw, color)
	fmt.Fprintf(&b, `<g fill="#fff" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle"><text x="%d" y="14">%s</text><text x="%d" y="14">%s</text></g></svg>`,
		lw/2, html.EscapeString(label), lw+vw/2, html.EscapeString(value))
	body := b.String()
	sum := sha256.Sum256([]byte(body))
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write([]byte(body))
}
