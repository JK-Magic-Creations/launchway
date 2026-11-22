package api

import (
	"context"
	"io"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// publishOnOutsideHost makes the release, with its notes and files, on the
// outside Git host of a Flight that keeps its code there (the tag was made
// there when the release was cut). A failure is audited and never undoes the
// release: it is Launchway's record; the host's page is a copy that can be
// made again by publishing the release again.
func (s *Server) publishOnOutsideHost(ctx context.Context, rel *engine.Release, actor string) {
	if s.Hosts == nil || !s.Hosts.IsAttached(rel.Project) {
		return
	}
	spec := forge.ReleaseSpec{Tag: rel.Tag, Title: nonEmpty(rel.Title, rel.Project+" "+rel.Version), Notes: rel.Notes, Prerelease: rel.Prerelease}
	for _, a := range rel.Assets {
		if a.Kind != engine.AssetFile {
			continue
		}
		name := a.Name
		spec.Files = append(spec.Files, forge.ReleaseFile{Name: name, Size: a.Size, Open: func(ctx context.Context) (rc io.ReadCloser, err error) {
			rc, _, err = s.Engine.OpenReleaseAsset(ctx, rel.ID, name)
			return rc, err
		}})
	}
	pctx, cancel := context.WithTimeout(ctx, outsideLimit)
	defer cancel()
	link, err := s.Hosts.PublishRelease(pctx, rel.Project, spec)
	e := &engine.AuditEntry{At: s.Now().UnixMilli(), User: actor, Action: "outside_host.release", Project: rel.Project, Target: releaseTarget(rel)}
	if err != nil {
		e.Outcome, e.Detail = "failed", clip(errText(err), 300)
	} else {
		e.Outcome, e.Detail = "ok", "release made on the outside host: "+clip(link, 200)
	}
	s.record(e)
}
