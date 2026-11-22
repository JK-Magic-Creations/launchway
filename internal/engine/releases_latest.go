package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// The stable "latest" of a project is its newest published release that is not a pre-release: a withdrawn
// (yanked) release, a draft and a candidate are never it. Newest is by the time of publication, so a patch
// release of an older line made today is the newest one made today.

// latestReleaseIDs returns the id of the latest release of each project (of one project when named).
func (e *Engine) latestReleaseIDs(project string) (map[string]string, error) {
	return store.LatestReleaseIDs(e.db, project)
}

// LatestRelease returns the latest release of a project: the newest published one that is not a pre-release.
func (e *Engine) LatestRelease(project string) (*Release, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	ids, err := e.latestReleaseIDs(project)
	if err != nil {
		return nil, err
	}
	id, ok := ids[project]
	if !ok {
		return nil, fmt.Errorf("%w: %s has no published release that is not a pre-release", ErrNotFound, project)
	}
	r, err := e.GetRelease(id)
	if err != nil {
		return nil, err
	}
	r.Latest = true
	return r, nil
}

// CountReleaseDownload adds one completed download of a release's file. A database that is older than schema 58
// has nowhere to keep it and counts nothing.
func (e *Engine) CountReleaseDownload(releaseID, asset string) error {
	if !e.knows(schemaTagRules) {
		return nil
	}
	return store.CountDownload(e.db, releaseID, asset, e.now().UnixMilli())
}

// releaseDownloads returns the download count of each file of a release, by name.
func (e *Engine) releaseDownloads(releaseID string) (map[string]int64, error) {
	if !e.knows(schemaTagRules) {
		return map[string]int64{}, nil
	}
	return store.DownloadsOf(e.db, releaseID)
}
