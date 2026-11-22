package store

import (
	"context"
	"database/sql"
)

// The statements of the hosting and release gaps closed together (schemas 58 and 59): the protected tags of a
// repository, the downloads counted per release file, the Flights that show their badges, the latest release of a
// Flight and the installation's hosting settings. Like release_outside's they run on the connection the caller hands
// in, until the engine and the forge move into the store.

// CtxExecer is an Execer that takes a context, as the forge's database does.
type CtxExecer interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// CtxQueryer reads rows with a context.
type CtxQueryer interface {
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// ---- protected tags ----

// TagRuleRow is one row of tag_rules; Pipelines is the JSON list the column holds.
type TagRuleRow struct {
	Pattern, CreateRole, ChangeRole, Pipelines string
}

// TagRulesOf returns a repository's protected tag rules in the order they were made.
func TagRulesOf(ctx context.Context, q CtxQueryer, repoID string) ([]TagRuleRow, error) {
	rows, err := q.Query(ctx, `SELECT pattern, create_role, change_role, pipelines FROM tag_rules WHERE repo_id = ? ORDER BY created_at, pattern`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TagRuleRow
	for rows.Next() {
		var r TagRuleRow
		if err := rows.Scan(&r.Pattern, &r.CreateRole, &r.ChangeRole, &r.Pipelines); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceTagRules deletes a repository's tag rules (the caller adds the new ones with AddTagRule).
func ReplaceTagRules(ctx context.Context, x CtxExecer, repoID string) error {
	_, err := x.Exec(ctx, `DELETE FROM tag_rules WHERE repo_id = ?`, repoID)
	return err
}

// AddTagRule adds one tag rule.
func AddTagRule(ctx context.Context, x CtxExecer, id, repoID string, r TagRuleRow, now int64, by string) error {
	_, err := x.Exec(ctx, `INSERT INTO tag_rules(id, repo_id, pattern, create_role, change_role, pipelines, created_at, created_by) VALUES(?,?,?,?,?,?,?,?)`,
		id, repoID, r.Pattern, r.CreateRole, r.ChangeRole, r.Pipelines, now, by)
	return err
}

// ---- downloads of release files ----

// CountDownload adds one completed download of a release's file.
func CountDownload(x Execer, releaseID, asset string, now int64) error {
	_, err := x.Exec(`INSERT INTO release_downloads(release_id, asset, downloads, last_at) VALUES(?,?,1,?)
		ON CONFLICT(release_id, asset) DO UPDATE SET downloads = release_downloads.downloads + 1, last_at = excluded.last_at`,
		releaseID, asset, now)
	return err
}

// DownloadsOf returns the download count of each file of a release, by name.
func DownloadsOf(q Queryer, releaseID string) (map[string]int64, error) {
	rows, err := q.Query(`SELECT asset, downloads FROM release_downloads WHERE release_id = ?`, releaseID)
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

// LatestReleaseIDs returns the id of the newest published release that is not a pre-release, per project (of one
// project when named).
func LatestReleaseIDs(q Queryer, project string) (map[string]string, error) {
	query, args := `SELECT project, id FROM releases WHERE state = 'published' AND prerelease = 0`, []any{}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	rows, err := q.Query(query+` ORDER BY published_at DESC, created_at DESC, id DESC`, args...)
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

// ---- Flight badges and hosting settings ----

// FlightBadgesOn says whether a Flight's badges are switched on.
func FlightBadgesOn(q Queryer, project string) (bool, error) {
	rows, err := q.Query(`SELECT enabled FROM flight_badges WHERE project = ?`, project)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	on := 0
	if rows.Next() {
		if err := rows.Scan(&on); err != nil {
			return false, err
		}
	}
	return on == 1, rows.Err()
}

// SetFlightBadgesOn switches a Flight's badges on or off.
func SetFlightBadgesOn(x Execer, project string, on bool, now int64, actor string) error {
	v := 0
	if on {
		v = 1
	}
	_, err := x.Exec(`INSERT INTO flight_badges(project, enabled, updated_at, updated_by) VALUES(?,?,?,?)
		ON CONFLICT(project) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		project, v, now, actor)
	return err
}

// HostingSettingOf returns a hosting setting's value and whether it was ever set.
func HostingSettingOf(q Queryer, name string) (string, bool, error) {
	rows, err := q.Query(`SELECT value FROM hosting_settings WHERE name = ?`, name)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	var v string
	err = rows.Scan(&v)
	return v, err == nil, err
}

// SetHostingSettingOf sets a hosting setting.
func SetHostingSettingOf(x Execer, name, value string, now int64, actor string) error {
	_, err := x.Exec(`INSERT INTO hosting_settings(name, value, updated_at, updated_by) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		name, value, now, actor)
	return err
}
