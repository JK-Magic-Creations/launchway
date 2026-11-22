package api

// The release published to an outside Git host (docs/development/release-publish-outside.md).
//
// A push mirror whose publish_releases setting is on has the Launchway release
// made there too, once it is published: same tag, title and notes, each file
// uploaded. The publish is a row of release_outside, taken up by the release
// worker (RunReleaseWork). It fails soft: the Launchway release stands, the row
// says failed with the reason (credentials removed), and a retry is one call
// away. A tag the mirror has not copied yet is waited for, with a growing delay,
// up to a limit.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/release/outside"
)

const (
	// outsideTries is how many times a tag the outside host does not have
	// yet is looked for before the publish is recorded as failed.
	outsideTries = 12
	// outsideLease keeps another server from taking a row being worked on;
	// outsideLimit bounds one publish, uploads included.
	outsideLease = 3 * time.Hour
	outsideLimit = 2 * time.Hour
)

// outsideWait is the delay after the n-th wait for the tag: 15 seconds,
// doubling, at most ten minutes. A variable so that a test need not wait.
var outsideWait = func(n int) time.Duration {
	d := 15 * time.Second
	for i := 1; i < n && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

func (s *Server) outsidePublisher() outside.Publisher {
	if s.Forge != nil && s.Forge.Outside != nil {
		return s.Forge.Outside
	}
	if s.Forge != nil && s.Forge.Mirrors != nil {
		return outside.NewREST(s.Forge.Mirrors.Guard())
	}
	return outside.NewREST(nil)
}

// queueOutside notes, for each push mirror of the release's repository that
// publishes releases, that the published release is to be made there. It
// never fails the publish: a problem is logged.
func (s *Server) queueOutside(ctx context.Context, rel *engine.Release) {
	if rel.State != engine.ReleasePublished || rel.Tag == "" || s.Forge == nil || s.Forge.Mirrors == nil {
		return
	}
	repo, ok := s.hostedRepoOfFlight(ctx, rel.Project)
	if !ok {
		return
	}
	ms, err := s.Forge.Mirrors.Of(ctx, repo.ID)
	if err != nil {
		s.Log.Warn("looking for the push mirrors that publish releases", "project", rel.Project, "err", err)
		return
	}
	for _, m := range ms {
		if m.Direction != mirror.Push || !m.PublishReleases || !m.Enabled {
			continue
		}
		if err := s.Engine.QueueReleaseOutside(rel.ID, m.ID); err != nil {
			s.Log.Warn("noting the release for an outside Git host", "project", rel.Project, "version", rel.Version, "err", err)
		}
	}
}

// runOutsideWork publishes the due rows until ctx ends.
func (s *Server) runOutsideWork(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.advanceOutside(ctx)
		}
	}
}

// advanceOutside publishes every row that is due, one after the other.
func (s *Server) advanceOutside(ctx context.Context) {
	rows, err := s.Engine.ReleaseOutsideDue(10)
	if err != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		if ok, err := s.Engine.ClaimReleaseOutside(row.ID, outsideLease.Milliseconds()); err != nil || !ok {
			continue
		}
		s.publishOutside(ctx, row)
	}
}

// outsideAudit records an attempt in the Logbook. The detail is the redacted reason.
func (s *Server) outsideAudit(rel *engine.Release, mirrorID, outcome, detail string) {
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "launchway", Source: "release worker", Action: "release.outside_publish",
		Project: rel.Project, Target: releaseTarget(rel), Outcome: outcome, Detail: "mirror " + mirrorID + ": " + detail})
}

// cleanOutside removes credentials from a reason and bounds its length.
func cleanOutside(msg string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) > 3 {
			msg = strings.ReplaceAll(msg, sec, "***")
		}
	}
	return clip(redact.URLs(msg), 600)
}

// publishOutside makes one release on one mirror's outside host and records
// what came of it.
func (s *Server) publishOutside(ctx context.Context, row engine.ReleaseOutside) {
	attempts := row.Attempts + 1
	rel, err := s.Engine.GetRelease(row.ReleaseID)
	if err != nil {
		_ = s.Engine.FinishReleaseOutside(row.ID, engine.OutsideFailed, "", "the release cannot be read: "+cleanOutside(err.Error()), attempts, 0)
		return
	}
	fail := func(reason string, secrets ...string) {
		reason = cleanOutside(reason, secrets...)
		_ = s.Engine.FinishReleaseOutside(row.ID, engine.OutsideFailed, "", reason, attempts, 0)
		s.outsideAudit(rel, row.MirrorID, "failed", reason)
		s.Log.Warn("a release could not be published on an outside Git host", "project", rel.Project, "version", rel.Version, "mirror", row.MirrorID, "reason", reason)
	}
	if s.Forge == nil || s.Forge.Mirrors == nil {
		fail("the mirror service is off")
		return
	}
	m, err := s.Forge.Mirrors.Get(ctx, row.MirrorID)
	switch {
	case err != nil:
		fail("the mirror no longer exists")
		return
	case m.Direction != mirror.Push || !m.PublishReleases:
		fail("publishing releases is off on this mirror; nothing was sent")
		return
	case rel.State != engine.ReleasePublished || rel.Tag == "":
		fail("the release is not published (state " + rel.State + "); nothing was sent")
		return
	}
	secret, _ := m.Credential.Resolve() // only to take it out of what is kept
	target := outside.Target{URL: m.URL, API: m.OutsideAPI, Username: m.Username, Credential: m.Credential}
	pub := outside.Release{Tag: rel.Tag, Title: nonEmpty(rel.Title, rel.Project+" "+rel.Version), Notes: rel.Notes, Prerelease: rel.Prerelease}
	for _, a := range rel.Assets {
		if a.Kind != engine.AssetFile {
			continue
		}
		name := a.Name
		pub.Files = append(pub.Files, outside.File{Name: name, Size: a.Size, Open: func(ctx context.Context) (rc io.ReadCloser, err error) {
			rc, _, err = s.Engine.OpenReleaseAsset(ctx, rel.ID, name)
			return rc, err
		}})
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outsideLimit)
	defer cancel()
	res, err := s.outsidePublisher().Publish(pctx, target, pub)
	switch {
	case err == nil:
		_ = s.Engine.FinishReleaseOutside(row.ID, engine.OutsidePublished, clip(res.URL, 500), "", attempts, 0)
		s.outsideAudit(rel, row.MirrorID, "ok", "published as "+res.URL)
	case errors.Is(err, outside.ErrTagMissing) && attempts < outsideTries:
		reason := cleanOutside("waiting for the mirror to copy the tag: "+err.Error(), secret)
		next := s.Now().Add(outsideWait(attempts)).UnixMilli()
		_ = s.Engine.FinishReleaseOutside(row.ID, engine.OutsidePending, "", reason, attempts, next)
		s.outsideAudit(rel, row.MirrorID, "waiting", fmt.Sprintf("attempt %d: %s", attempts, reason))
	default:
		fail(err.Error(), secret)
	}
}

// ---------------------------------------------------------------- views and the retry

// outsideView is a release's state on one mirror's outside host, for people.
type outsideView struct {
	engine.ReleaseOutside
	MirrorURL string `json:"mirror_url,omitempty"`
	// Repository is the hosted repository the mirror belongs to.
	Repository string `json:"repository,omitempty"`
}

func (s *Server) outsideViews(r *http.Request, rel *engine.Release) []outsideView {
	rows, err := s.Engine.ReleaseOutsideOf(rel.ID)
	out := make([]outsideView, 0, len(rows))
	if err != nil {
		return out
	}
	for _, row := range rows {
		v := outsideView{ReleaseOutside: row}
		if s.Forge != nil && s.Forge.Mirrors != nil {
			if m, err := s.Forge.Mirrors.Get(r.Context(), row.MirrorID); err == nil {
				v.MirrorURL, v.Repository = m.URL, s.Forge.Mirrors.View(r.Context(), m).Repository
			}
		}
		out = append(out, v)
	}
	return out
}

// outsideStates fills the Outside list of mirror views: how each mirror's
// latest releases stand on its outside host.
func (s *Server) outsideStates(vs []mirror.View) {
	for i := range vs {
		v := &vs[i]
		if v.Direction != mirror.Push || !v.PublishReleases {
			continue
		}
		rows, err := s.Engine.ReleaseOutsideOfMirror(v.ID, 3)
		if err != nil {
			continue
		}
		for _, row := range rows {
			st := mirror.OutsideState{Release: row.ReleaseID, State: row.State, URL: row.URL, Reason: row.Reason, Attempts: row.Attempts,
				UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC()}
			if rel, err := s.Engine.GetRelease(row.ReleaseID); err == nil {
				st.Version = rel.Project + " " + rel.Version
			}
			v.Outside = append(v.Outside, st)
		}
	}
}

// retryOutside makes a failed outside publish pending again, or queues the
// release for a mirror that has none yet (a mirror turned on after the
// publish). The worker does the sending.
func (s *Server) retryOutside(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	mid := r.PathValue("mirror")
	setAudit(r, releaseTarget(rel), "publish on the outside Git host again, mirror "+mid)
	if rel.State != engine.ReleasePublished {
		writeErr(w, http.StatusConflict, "only a published release is made on an outside Git host; this one is "+rel.State)
		return
	}
	if s.Forge == nil || s.Forge.Mirrors == nil {
		writeFeatureOff(w)
		return
	}
	row, found, err := s.Engine.ReleaseOutsideState(rel.ID, mid)
	if err != nil {
		s.fail(w, err)
		return
	}
	switch {
	case found && row.State == engine.OutsideFailed:
		if _, err := s.Engine.RetryReleaseOutside(rel.ID, mid); err != nil {
			s.fail(w, err)
			return
		}
	case found:
		writeErr(w, http.StatusConflict, "the release is "+row.State+" on that mirror; only a failed publish is retried")
		return
	default:
		m, err := s.Forge.Mirrors.Get(r.Context(), mid)
		repo, hosted := s.hostedRepoOfFlight(r.Context(), rel.Project)
		if err != nil || !hosted || m.RepoID != repo.ID || m.Direction != mirror.Push {
			writeErr(w, http.StatusNotFound, rel.Project+" has no push mirror "+mid)
			return
		}
		if !m.PublishReleases {
			writeErr(w, http.StatusConflict, "that mirror does not publish releases: turn on its publish_releases setting first")
			return
		}
		if err := s.Engine.QueueReleaseOutside(rel.ID, mid); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"outside": s.outsideViews(r, rel)})
}
