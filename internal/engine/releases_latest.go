package engine

import "fmt"

// The stable "latest" of a project is its newest published release that is not a pre-release: a withdrawn
// (yanked) release, a draft and a candidate are never it. Newest is by the time of publication, so a patch
// release of an older line made today is the newest one made today.

// latestReleaseIDs returns the id of the latest release of each project (of one project when named).
func (e *Engine) latestReleaseIDs(project string) (map[string]string, error) {
	q, args := `SELECT project, id FROM releases WHERE state = ? AND prerelease = 0`, []any{ReleasePublished}
	if project != "" {
		q += ` AND project = ?`
		args = append(args, project)
	}
	rows, err := e.db.Query(q+` ORDER BY published_at DESC, created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			return nil, err
		}
		if _, seen := out[p]; !seen {
			out[p] = id
		}
	}
	return out, rows.Err()
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
	_, err := e.db.Exec(`INSERT INTO release_downloads(release_id, asset, downloads, last_at) VALUES(?,?,1,?)
		ON CONFLICT(release_id, asset) DO UPDATE SET downloads = release_downloads.downloads + 1, last_at = excluded.last_at`,
		releaseID, asset, e.now().UnixMilli())
	return err
}

// releaseDownloads returns the download count of each file of a release, by name.
func (e *Engine) releaseDownloads(releaseID string) (map[string]int64, error) {
	if !e.knows(schemaTagRules) {
		return map[string]int64{}, nil
	}
	rows, err := e.db.Query(`SELECT asset, downloads FROM release_downloads WHERE release_id = ?`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}
