package engine

import (
	"fmt"
	"strings"
	"time"
)

// A person's trail: what they opened and what they did there, kept on the server so it follows them across
// devices. It is private to the person: nothing here takes another user's name except to write or read the
// caller's own entries, and the API never offers a way to read someone else's.

// Activity is one entry of the trail.
type Activity struct {
	Kind    string `json:"kind"`    // run, project, pipeline, repository, pull, deployment, settings, page
	Obj     string `json:"obj"`     // what identifies it within its kind (a run's ID, a project's name, a path)
	Project string `json:"project"` // the project it belongs to, for permission; "" when none
	Title   string `json:"title"`
	Href    string `json:"href"`
	Action  string `json:"action"` // viewed, started, approved, commented, edited
	State   string `json:"state"`  // what it looked like then, to say what changed since
	At      int64  `json:"at"`
}

// ActivityKinds are the kinds the trail keeps.
var ActivityKinds = []string{"run", "project", "pipeline", "repository", "pull", "deployment", "settings", "page"}

const activityKeep = 400 // entries kept for one person

// RecordActivity writes or moves an entry of the person's trail. Opening the same thing again moves its entry to the top,
// keeping the strongest action it has had lately (an edit is not forgotten by a view a minute later).
func (e *Engine) RecordActivity(user string, a Activity, retentionDays int) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	ok := false
	for _, k := range ActivityKinds {
		ok = ok || k == a.Kind
	}
	if user == "" || !ok || a.Obj == "" || len(a.Obj) > 300 || len(a.Title) > 200 || len(a.Href) > 500 || !strings.HasPrefix(a.Href, "/") {
		return fmt.Errorf("%w: an entry needs a kind, an object, a title and an address on this server", ErrInvalid)
	}
	a.At = e.now().UnixMilli()
	if a.Action == "" {
		a.Action = "viewed"
	}
	unlock := e.writeLock()
	defer unlock()
	var old string
	_ = e.db.QueryRow(`SELECT action FROM org_activity WHERE username=? AND kind=? AND obj=? AND at > ?`, user, a.Kind, a.Obj, a.At-int64(30*time.Minute/time.Millisecond)).Scan(&old)
	if a.Action == "viewed" && old != "" {
		a.Action = old
	}
	if _, err := e.db.Exec(`DELETE FROM org_activity WHERE username=? AND kind=? AND obj=?`, user, a.Kind, a.Obj); err != nil {
		return err
	}
	if _, err := e.db.Exec(`INSERT INTO org_activity(username,kind,obj,project,title,href,action,state,at) VALUES(?,?,?,?,?,?,?,?,?)`,
		user, a.Kind, a.Obj, a.Project, a.Title, a.Href, a.Action, a.State, a.At); err != nil {
		return err
	}
	// Retention and the cap, so the table stays small.
	if retentionDays > 0 {
		if _, err := e.db.Exec(`DELETE FROM org_activity WHERE username=? AND at < ?`, user, a.At-int64(retentionDays)*24*int64(time.Hour/time.Millisecond)); err != nil {
			return err
		}
	}
	_, err := e.db.Exec(`DELETE FROM org_activity WHERE username=? AND at < (SELECT MIN(at) FROM (SELECT at FROM org_activity WHERE username=? ORDER BY at DESC LIMIT ?) t)`, user, user, activityKeep)
	return err
}

// ActivityOf lists the person's own trail, newest first, leaving out what is older than retentionDays (0: no limit).
func (e *Engine) ActivityOf(user string, retentionDays, limit int) ([]Activity, error) {
	if !e.knows(schemaOrganize) || user == "" {
		return []Activity{}, nil
	}
	if limit <= 0 || limit > activityKeep {
		limit = activityKeep
	}
	since := int64(0)
	if retentionDays > 0 {
		since = e.now().UnixMilli() - int64(retentionDays)*24*int64(time.Hour/time.Millisecond)
	}
	rows, err := e.db.Query(`SELECT kind,obj,project,title,href,action,state,at FROM org_activity WHERE username=? AND at >= ? ORDER BY at DESC LIMIT ?`, user, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.Kind, &a.Obj, &a.Project, &a.Title, &a.Href, &a.Action, &a.State, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClearActivity deletes the person's whole trail.
func (e *Engine) ClearActivity(user string) error {
	if err := e.orgReady(); err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`DELETE FROM org_activity WHERE username=?`, user)
	return err
}
