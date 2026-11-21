package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// What a push says about pull requests, the same over HTTPS and SSH. After a
// push to a branch that has no open pull request, git prints one plain line
// with the address to open one. The push options -o pr.create, -o pr.title=...
// and -o pr.target=... open it as the push finishes. See docs/guide/pull-requests.md.

// pushPR is what the push options of one push ask for.
type pushPR struct {
	create bool
	title  string
	target string
}

func parsePushOptions(opts []string) pushPR {
	var o pushPR
	for _, opt := range opts {
		switch k, v, _ := strings.Cut(opt, "="); k {
		case "pr.create":
			o.create = v == "" || v == "true" || v == "1"
		case "pr.title":
			o.title = strings.TrimSpace(v)
		case "pr.target":
			o.target = strings.TrimSpace(v)
		}
	}
	return o
}

// pushBranches are the branches of a push that could use a pull request: new
// or moved, not deleted, and not the default branch.
func pushBranches(repo forge.Repository, ups []githttp.RefUpdate) []string {
	var out []string
	for _, u := range ups {
		br, ok := strings.CutPrefix(u.Name, "refs/heads/")
		if ok && !zeroSHA(u.New) && br != repo.DefaultBranch {
			out = append(out, br)
		}
	}
	return out
}

// webBase is the address the interface is served at, from the repository's
// HTTPS clone address (which the installation's base URL makes), or from the
// request when the push came over HTTP. Empty when neither is known.
func webBase(repo forge.Repository, r *http.Request, https func(*http.Request) bool) string {
	if c := repo.Clone.HTTPS; c != "" {
		if i := strings.Index(c, "/git/"); i > 0 {
			return c[:i]
		}
	}
	if r != nil && r.Host != "" {
		scheme := "http"
		if https(r) {
			scheme = "https"
		}
		return scheme + "://" + r.Host
	}
	return ""
}

// pushNotice is the Notice of a push: the line that tells the pusher where to
// open a pull request, or that one will be opened. It reads the database only.
func (s *Server) pushNotice(c gitCaller, repo forge.Repository, r *http.Request) func(context.Context, []githttp.RefUpdate, []string) string {
	if s.Forge == nil || s.Forge.Pulls == nil || repo.Archived {
		return nil
	}
	base := webBase(repo, r, s.isHTTPS)
	return func(ctx context.Context, ups []githttp.RefUpdate, opts []string) string {
		want := parsePushOptions(opts)
		var lines []string
		for _, br := range pushBranches(repo, ups) {
			if open, err := s.Forge.Pulls.Store.OpenFromSource(ctx, repo.ID, br); err != nil || len(open) > 0 {
				continue
			}
			target := want.target
			if target == "" {
				target = repo.DefaultBranch
			}
			page := base + "/repositories/" + repo.Path + "/pulls"
			if base == "" {
				page = "the Pull requests page of " + repo.Path + " in Launchway"
			}
			switch {
			case want.create && c.id != nil:
				lines = append(lines, fmt.Sprintf("Opening a pull request from %s into %s as this push finishes: %s", br, target, page))
			case base != "":
				lines = append(lines, fmt.Sprintf("Open a pull request for %s: %s/new?head=%s (or push again with -o pr.create)", br, page, url.QueryEscape(br)))
			default:
				lines = append(lines, fmt.Sprintf("Open a pull request for %s from %s (or push again with -o pr.create)", br, page))
			}
			if len(lines) == 3 {
				break
			}
		}
		return strings.Join(lines, "\n")
	}
}

// openRequestedPulls opens the pull requests a push asked for with -o
// pr.create, after the push has finished (the repository's files are not
// readable while the push holds them). Anything that goes wrong is in the
// audit log: the push itself is done and stays done.
func (s *Server) openRequestedPulls(ctx context.Context, c gitCaller, repo forge.Repository, rr *storage.ReceiveResult) {
	if rr == nil || !rr.Committed || s.Forge == nil || s.Forge.Pulls == nil || c.id == nil || c.user == "" || repo.Archived {
		return
	}
	want := parsePushOptions(rr.Options)
	if !want.create {
		return
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second) // the push is done: the client hanging up changes nothing
	defer cancel()
	for _, br := range pushBranches(repo, rr.Updates) {
		if open, err := s.Forge.Pulls.Store.OpenFromSource(ctx, repo.ID, br); err != nil || len(open) > 0 {
			continue
		}
		target := want.target
		if target == "" {
			target = repo.DefaultBranch
		}
		title := want.title
		if title == "" {
			title = strings.ToUpper(br[:1]) + strings.NewReplacer("-", " ", "_", " ").Replace(br[1:])
		}
		e := &engine.AuditEntry{At: s.Now().UnixMilli(), User: c.user, Source: "git push", Action: "pull.create", Target: repo.Path, Outcome: "ok"}
		p, err := s.Forge.Pulls.Open(ctx, repo.ID, c.user, pulls.New{Title: title, SourceBranch: br, TargetBranch: target})
		if err != nil {
			e.Outcome, e.Detail = "failed", fmt.Sprintf("%s into %s by push option: %v", br, target, err)
		} else {
			e.Detail = fmt.Sprintf("#%d %s into %s, by push option pr.create", p.Number, br, target)
		}
		s.record(e)
	}
}
