package api

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// What happens once something has happened to a hosted repository, whichever
// way it was done (HTTPS, SSH, the API): the webhooks that asked are queued,
// and a push makes the projects that watch the repository look at it at once,
// through the trigger path every Git host uses (a poll that finds the moved
// ref and starts the pipeline), not through a webhook to ourselves.

func (s *Server) hookRepo(r forge.Repository) webhooks.Repo {
	return webhooks.Repo{Path: r.Path, Owner: r.Home, Name: r.Name, DefaultBranch: r.DefaultBranch, Visibility: string(r.Visibility),
		CloneHTTPS: r.Clone.HTTPS, CloneSSH: r.Clone.SSH}
}

func zeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// afterPush is called once a push has committed.
func (s *Server) afterPush(ctx context.Context, c gitCaller, repo forge.Repository, updates []githttp.RefUpdate) {
	// The push has committed: what follows is not undone when the client hangs up.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if s.Forge != nil && s.Forge.Hooks != nil {
		for _, u := range updates {
			s.emitRefEvents(ctx, c, repo, u)
		}
	}
	s.pollWatchers(repo)
}

func (s *Server) emitRefEvents(ctx context.Context, c gitCaller, repo forge.Repository, u githttp.RefUpdate) {
	kind := "branch"
	switch {
	case strings.HasPrefix(u.Name, "refs/tags/"):
		kind = "tag"
	case !strings.HasPrefix(u.Name, "refs/heads/"):
		return // other refs (pull-request heads and the like) are not events yet
	}
	base := webhooks.Payload{Repository: s.hookRepo(repo), Sender: webhooks.Sender{Name: c.user}, Ref: u.Name, Before: u.Old, After: u.New}
	hooks := s.Forge.Hooks
	switch {
	case zeroSHA(u.New):
		p := base
		p.Deleted, p.RefType = true, kind
		hooks.Emit(ctx, repo.ID, repo.Home, webhooks.EventDelete, p)
	default:
		if zeroSHA(u.Old) {
			p := base
			p.Created, p.RefType = true, kind
			hooks.Emit(ctx, repo.ID, repo.Home, webhooks.EventCreate, p)
		}
		p := base
		p.Created = zeroSHA(u.Old)
		hooks.Emit(ctx, repo.ID, repo.Home, webhooks.EventPush, p)
	}
}

// emitRepository tells the webhooks that a repository was created, renamed or
// deleted. changes carries what is useful to a receiver (renamed: the old path).
func (s *Server) emitRepository(ctx context.Context, by string, repo forge.Repository, action string, changes map[string]string) {
	if s.Forge == nil || s.Forge.Hooks == nil {
		return
	}
	p := webhooks.Payload{Repository: s.hookRepo(repo), Sender: webhooks.Sender{Name: by}, Action: action, Changes: changes}
	s.Forge.Hooks.Emit(ctx, repo.ID, repo.Home, webhooks.EventRepository, p)
}

// pollWatchers asks every project that watches this repository to be looked
// at now. Projects find their repository by address: the path of the address
// is /git/<owner>/<name> or /<owner>/<name>, with or without .git. A project
// polled without need only wastes one poll.
func (s *Server) pollWatchers(repo forge.Repository) {
	if s.Projects == nil {
		return
	}
	for _, p := range s.Projects.List() {
		if addressIsRepo(p.URL, repo.Path) {
			s.Projects.PollNow(p.Name)
		}
	}
}

func addressIsRepo(addr, repoPath string) bool {
	path := addr
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		path = u.Path
	} else if _, after, ok := strings.Cut(addr, ":"); ok { // scp style: git@host:owner/name.git
		path = after
	}
	path = "/" + strings.Trim(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/")
	return path == "/"+repoPath || strings.HasSuffix(path, "/git/"+repoPath) || strings.HasSuffix(path, "/"+repoPath)
}

// WebhookDisabled records in the audit trail that a webhook was turned off
// because its deliveries kept failing (hosting passes it to the delivery
// service as its OnDisabled).
func (s *Server) WebhookDisabled(h webhooks.Hook, reason string) {
	target := h.Repository
	if target == "" {
		target = h.Owner
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "system", Action: "webhook.disable", Target: target, Outcome: "ok",
		Detail: "webhook " + h.Name + ": " + reason})
}
