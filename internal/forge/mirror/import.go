package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// SourceRequest says which repositories of which server a call is about.
type SourceRequest struct {
	// Connection is the name of a connection of kind gitea, forgejo or plain.
	// Without one, URLs are read with no credential.
	Connection string `json:"connection,omitempty"`
	// URLs is a plain list of repository addresses (the source of a plain
	// connection, or of no connection).
	URLs []string `json:"urls,omitempty"`
	// Owner limits a listing to one user or organization of the source;
	// Repository ("owner/name") to one repository.
	Owner      string `json:"owner,omitempty"`
	Repository string `json:"repository,omitempty"`
	// TargetOwner puts everything under another owner here (default: the
	// source's owner names are kept).
	TargetOwner string `json:"target_owner,omitempty"`
	// Username goes with the connection's credential on the mirror it makes.
	Username string `json:"username,omitempty"`
}

// ImportRequest is a request to bring repositories in.
type ImportRequest struct {
	SourceRequest
	// Mirror leaves each repository a pull mirror of its source (the default,
	// the staged move); false makes each an ordinary repository once it has
	// been copied and checked.
	Mirror *bool `json:"mirror,omitempty"`
	// IntervalSeconds is the mirrors' schedule (default 1 hour).
	IntervalSeconds int64 `json:"interval_seconds,omitempty"`
	// DryRun lists what would be created and changes nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Wait copies and checks each repository before answering; without it
	// the repositories are made and their mirrors left to run in the background.
	Wait bool `json:"wait,omitempty"`
}

// Import item actions and states.
const (
	ActionCreate = "create"
	ActionResume = "resume"
	ActionSkip   = "skip"

	StatePlanned  = "planned"
	StateQueued   = "queued"
	StateVerified = "verified"
	StateSkipped  = "skipped"
	StateFailed   = "failed"
)

// ImportItem is what happened, or would, to one repository.
type ImportItem struct {
	Source     string           `json:"source"`
	Repository string           `json:"repository"`
	URL        string           `json:"url"`
	Visibility forge.Visibility `json:"visibility"`
	Action     string           `json:"action"`
	State      string           `json:"state"`
	// Reason says why a repository is skipped or what a dry run would do.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// SourceRefs and HostedRefs are the branches, tags and notes each side has
	// after the copy; Verified says every ref is equal; Differences names
	// those that are not.
	SourceRefs  int      `json:"source_refs,omitempty"`
	HostedRefs  int      `json:"hosted_refs,omitempty"`
	Verified    bool     `json:"verified,omitempty"`
	Differences []string `json:"differences,omitempty"`
	MirrorID    string   `json:"mirror_id,omitempty"`
}

// ImportResult is the answer to an import.
type ImportResult struct {
	DryRun  bool           `json:"dry_run"`
	Items   []ImportItem   `json:"items"`
	Summary map[string]int `json:"summary"`
}

func summarize[T any](items []T, key func(T) string) map[string]int {
	m := map[string]int{"total": len(items)}
	for _, it := range items {
		m[key(it)]++
	}
	return m
}

// plan lists the repositories of a source and what each maps to here.
type planned struct {
	src    SourceRepo
	target string
	cred   forge.SecretRef
	user   string
	err    string
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func (s *Service) plan(ctx context.Context, req SourceRequest) ([]planned, error) {
	var src Source
	var cred forge.SecretRef
	switch {
	case req.Connection != "":
		conn, err := s.st.ConnectionByName(ctx, req.Connection)
		if err != nil {
			return nil, err
		}
		if !conn.Enabled {
			return nil, fmt.Errorf("%w: the connection %s is turned off", forge.ErrInvalid, conn.Name)
		}
		cred = conn.Credential
		switch conn.Kind {
		case forge.KindGitea, forge.KindForgejo:
			api, err := NewGiteaAPI(conn, s.o.Guard)
			if err != nil {
				return nil, err
			}
			src = api
		case forge.KindPlain:
			src = URLList{URLs: req.URLs, Credentialed: hostOf(conn.BaseURL)}
		default:
			return nil, fmt.Errorf("%w: import reads repositories from connections of kind gitea, forgejo or plain, not %s", forge.ErrInvalid, conn.Kind)
		}
	case len(req.URLs) > 0:
		src = URLList{URLs: req.URLs}
	default:
		return nil, fmt.Errorf("%w: say where to import from: a connection, or a list of addresses (urls)", forge.ErrInvalid)
	}
	if _, plainList := src.(URLList); plainList && len(req.URLs) == 0 {
		return nil, fmt.Errorf("%w: a plain connection needs the addresses to import (urls)", forge.ErrInvalid)
	}
	all, err := src.List(ctx, req.Owner)
	if err != nil {
		return nil, err
	}
	var out []planned
	for _, r := range all {
		if req.Repository != "" && r.Path() != req.Repository {
			continue
		}
		p := planned{src: r, target: r.Path(), user: req.Username}
		if req.TargetOwner != "" {
			p.target = req.TargetOwner + "/" + r.Name
		}
		switch {
		case cred.IsZero():
		case src.Host() != "" && hostOf(r.URL) == strings.ToLower(src.Host()):
			p.cred = cred
		default:
			p.err = fmt.Sprintf("its address is on %s, not on the connection's host %s, so the connection's credential is not sent there", hostOf(r.URL), src.Host())
		}
		if p.err == "" {
			if u, perr := url.Parse(r.URL); perr != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
				p.err = "its clone address is not a plain http or https address without credentials in it"
			} else if gerr := s.o.Guard.Check(ctx, r.URL); gerr != nil {
				p.err = gerr.Error()
			}
		}
		out = append(out, p)
	}
	if req.Repository != "" && len(out) == 0 {
		return nil, fmt.Errorf("%w: the source has no repository %s", forge.ErrNotFound, req.Repository)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].target < out[j].target })
	return out, nil
}

// Import brings repositories in from a source: each is made with the same
// name and visibility, copied ref by ref, checked, and (by default) left a pull
// mirror. It is safe to run again: a repository already made as a mirror of the
// same address is resumed, and one that exists in any other state is left
// alone. A dry run changes nothing.
func (s *Service) Import(ctx context.Context, req ImportRequest, by string) (ImportResult, error) {
	res := ImportResult{DryRun: req.DryRun}
	keep := req.Mirror == nil || *req.Mirror
	if !keep && !req.Wait && !req.DryRun {
		return res, fmt.Errorf("%w: a repository can only be made an ordinary one after it has been copied and checked; ask to wait for the copy (wait), or leave them mirrors", forge.ErrInvalid)
	}
	if s.o.Creator == nil && !req.DryRun {
		return res, errors.New("this server cannot make repositories")
	}
	ps, err := s.plan(ctx, req.SourceRequest)
	if err != nil {
		return res, err
	}
	for _, p := range ps {
		it := s.importOne(ctx, p, req, keep, by)
		res.Items = append(res.Items, it)
	}
	res.Summary = summarize(res.Items, func(i ImportItem) string { return i.State })
	return res, nil
}

func (s *Service) importOne(ctx context.Context, p planned, req ImportRequest, keep bool, by string) ImportItem {
	it := ImportItem{Source: p.src.Path(), Repository: p.target, URL: p.src.URL, Visibility: p.src.Visibility}
	skip := func(why string) ImportItem { it.Action, it.State, it.Reason = ActionSkip, StateSkipped, why; return it }
	fail := func(err error) ImportItem {
		it.State, it.Error = StateFailed, redact.URLs(err.Error())
		return it
	}
	if p.err != "" {
		return skip(p.err)
	}
	spec := Spec{URL: p.src.URL, Username: p.user, Credential: p.cred, IntervalSeconds: req.IntervalSeconds}
	existing, err := s.repoByPath(ctx, p.target)
	switch {
	case err == nil:
		m, perr := s.PullOf(ctx, existing.ID)
		if !existing.Mirror || perr != nil || m.URL != p.src.URL {
			return skip("a repository of that name exists and is not a mirror of this address; it is left alone")
		}
		it.Action, it.MirrorID = ActionResume, m.ID
	case errors.Is(err, forge.ErrNotFound):
		it.Action = ActionCreate
	default:
		return fail(err)
	}
	if req.DryRun {
		it.State = StatePlanned
		if it.Action == ActionCreate {
			it.Reason = "would be created " + string(p.src.Visibility) + (map[bool]string{true: " and left a pull mirror", false: " and copied once"}[keep])
		} else {
			it.Reason = "already a mirror of this address; would be synced and checked again"
		}
		return it
	}
	if it.Action == ActionCreate {
		home, name, _ := strings.Cut(p.target, "/")
		repo, cerr := s.o.Creator.Create(ctx, forge.NewRepository{Home: home, Name: name, Visibility: p.src.Visibility, DefaultBranch: p.src.DefaultBranch})
		if cerr != nil {
			return fail(cerr)
		}
		m, merr := s.SetPull(ctx, repo.Path, spec, by)
		if merr != nil {
			// Do not leave an empty repository that is not a mirror behind to be
			// mistaken for a finished import.
			_ = s.st.TrashRepository(context.WithoutCancel(ctx), repo.ID)
			return fail(merr)
		}
		existing, it.MirrorID = repo, m.ID
	}
	it.State = StateQueued
	if !req.Wait {
		return it
	}
	if _, serr := s.SyncNow(ctx, it.MirrorID); serr != nil {
		return fail(serr)
	}
	cmp, verr := s.compare(ctx, existing, p.src.URL, p.cred, p.user)
	it.SourceRefs, it.HostedRefs, it.Verified, it.Differences = cmp.SourceRefs, cmp.HostedRefs, cmp.Equal(), cmp.Differences
	if verr != nil {
		return fail(verr)
	}
	if !it.Verified {
		return fail(errors.New("the copy differs from the source (see differences); the repository stays a mirror and can be synced again"))
	}
	it.State = StateVerified
	if !keep {
		if _, err := s.Promote(ctx, existing.Path, false, by); err != nil {
			return fail(err)
		}
		it.MirrorID = ""
	}
	return it
}

// ---------------------------------------------------------------- comparing

// Comparison is a repository's refs against its source's.
type Comparison struct {
	SourceRefs, HostedRefs int
	// Differences name the refs that are missing here, extra here or
	// at another commit, at most 20.
	Differences []string
	more        int
}

// Equal reports whether every ref is the same on both sides.
func (c Comparison) Equal() bool { return len(c.Differences) == 0 && c.more == 0 }

const maxDifferences = 20

func (s *Service) lsRemote(ctx context.Context, url string, cred forge.SecretRef, user string) (map[string]string, error) {
	if err := s.o.Guard.Check(ctx, url); err != nil {
		return nil, err
	}
	r, err := s.remoteFor(Mirror{Credential: cred, Username: user})
	if err != nil {
		return nil, err
	}
	out, err := s.git(ctx, r, "", 5*time.Minute, "ls-remote", url)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && hasManaged(name) && !strings.HasSuffix(name, "^{}") {
			refs[name] = sha
		}
	}
	return refs, nil
}

// compare reads the source's refs and this repository's refs of record.
func (s *Service) compare(ctx context.Context, repo forge.Repository, url string, cred forge.SecretRef, user string) (Comparison, error) {
	var c Comparison
	theirs, err := s.lsRemote(ctx, url, cred, user)
	if err != nil {
		return c, err
	}
	all, err := s.sto.Refs(ctx, repo.ID)
	if err != nil {
		return c, err
	}
	ours := map[string]string{}
	for n, v := range all {
		if hasManaged(n) {
			ours[n] = v
		}
	}
	c.SourceRefs, c.HostedRefs = len(theirs), len(ours)
	note := func(msg string) {
		if len(c.Differences) < maxDifferences {
			c.Differences = append(c.Differences, msg)
		} else {
			c.more++
		}
	}
	names := make([]string, 0, len(theirs)+len(ours))
	for n := range theirs {
		names = append(names, n)
	}
	for n := range ours {
		if _, ok := theirs[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		a, inSource := theirs[n]
		b, inHosted := ours[n]
		switch {
		case inSource && !inHosted:
			note(n + ": missing here")
		case !inSource:
			note(n + ": not in the source")
		case a != b:
			note(fmt.Sprintf("%s: source %.8s, here %.8s", n, a, b))
		}
	}
	return c, nil
}

// ---------------------------------------------------------------- connections

// ConnectionView is a connection as shown: where its credential is read
// from, never the credential.
type ConnectionView struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url,omitempty"`
	Credential string `json:"credential,omitempty"`
	Enabled    bool   `json:"enabled"`
}

func viewOf(c forge.Connection) ConnectionView {
	return ConnectionView{Name: c.Name, Kind: string(c.Kind), BaseURL: c.BaseURL, Credential: c.Credential.String(), Enabled: c.Enabled}
}

// importKinds are the kinds of connection an import reads from.
var importKinds = []forge.Kind{forge.KindGitea, forge.KindForgejo, forge.KindPlain}

// AddConnection records a source server: a name, a kind, the server's address
// (checked against the egress guard) and a credential reference.
func (s *Service) AddConnection(ctx context.Context, name string, kind forge.Kind, baseURL string, cred forge.SecretRef, by string) (ConnectionView, error) {
	ok := false
	for _, k := range importKinds {
		ok = ok || k == kind
	}
	if !ok {
		return ConnectionView{}, fmt.Errorf("%w: a connection to import from is of kind gitea, forgejo or plain, not %q", forge.ErrInvalid, kind)
	}
	if baseURL != "" {
		if err := s.o.Guard.Check(ctx, baseURL); err != nil {
			return ConnectionView{}, fmt.Errorf("%w: connection: %v", forge.ErrInvalid, err)
		}
	}
	c, err := s.st.CreateConnection(ctx, forge.Connection{Name: name, Kind: kind, BaseURL: baseURL, Credential: cred, Events: forge.EventsPolling, Enabled: true}, by)
	if err != nil {
		return ConnectionView{}, err
	}
	return viewOf(c), nil
}

// Connections lists the connections an import may read from.
func (s *Service) Connections(ctx context.Context) ([]ConnectionView, error) {
	all, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	out := []ConnectionView{}
	for _, c := range all {
		for _, k := range importKinds {
			if c.Kind == k {
				out = append(out, viewOf(c))
			}
		}
	}
	return out, nil
}

// RemoveConnection deletes a source connection (never the hosting one).
func (s *Service) RemoveConnection(ctx context.Context, name string) error {
	c, err := s.st.ConnectionByName(ctx, name)
	if err != nil {
		return err
	}
	if c.ID == s.conn.ID {
		return fmt.Errorf("%w: %s is Launchway's own hosting connection", forge.ErrInvalid, name)
	}
	return s.st.DeleteConnection(ctx, c.ID)
}
