package engine

import (
	"github.com/jk-magic-creations/launchway/internal/accept"
	"slices"
	"strings"
)

// RunnerView is a runner as last seen.
type RunnerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Version  string `json:"version"`
	LastSeen int64  `json:"last_seen"`
	Online   bool   `json:"online"` // seen within RunnerOffline
	// Pool and Targets, for a deploy runner: its runner pool, and the
	// targets it said it serves.
	Pool    string   `json:"pool,omitempty"`
	Targets []string `json:"targets,omitempty"`
	// Images says the runner builds images (it has image builders).
	Images bool `json:"images,omitempty"`

	// Schema 20 and 21. Kind is build or deploy. Auth is how it
	// authenticated: identity (an enrolled runner), shared (the shared
	// runner token, which says nothing about which runner it is) or pool (a
	// deploy pool's token). Pool is "default" for build runners of the
	// default pool.
	Kind   string         `json:"kind,omitempty"`
	Auth   string         `json:"auth,omitempty"`
	Labels []string       `json:"labels,omitempty"`
	OS     string         `json:"os,omitempty"`
	Arch   string         `json:"arch,omitempty"`
	Accept *accept.Config `json:"accept,omitempty"`
	// Identity is the enrolled runner, for one that is.
	Identity *RunnerIdentity `json:"identity,omitempty"`
	// Work is what it holds now.
	Work []RunnerWork `json:"work"`
}

// RunnerWork is a step a runner holds.
type RunnerWork struct {
	Run     string `json:"run"`
	Project string `json:"project"`
	Number  int    `json:"number"`
	Step    string `json:"step"`
}

// Runners lists runners, most recently seen first. Runners not seen for a
// day are forgotten and not listed.
func (e *Engine) Runners() ([]RunnerView, error) {
	if e.knows(schemaIdentity) {
		return e.runnersWithIdentity()
	}
	extra := `, '', '', ''`
	switch {
	case e.knows(schemaImages):
		extra = `, pool, targets, features`
	case e.knows(schemaDeploy):
		extra = `, pool, targets, ''`
	}
	rows, err := e.db.Query(`SELECT id, name, executor, version, last_seen` + extra + ` FROM runners ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	online := e.now().Add(-RunnerOffline).UnixMilli()
	out := []RunnerView{}
	for rows.Next() {
		var r RunnerView
		var targets, features string
		if err := rows.Scan(&r.ID, &r.Name, &r.Executor, &r.Version, &r.LastSeen, &r.Pool, &targets, &features); err != nil {
			return nil, err
		}
		r.Images = slices.Contains(strings.Fields(features), FeatureImages)
		if targets != "" {
			r.Targets = strings.Split(targets, ",")
		}
		r.Online = r.LastSeen >= online
		out = append(out, r)
	}
	return out, rows.Err()
}

// runnersWithIdentity lists runners with what schema 20 records: enrolled runners
// that have not asked for work yet (or were forgotten) included.
func (e *Engine) runnersWithIdentity() ([]RunnerView, error) {
	rows, err := e.db.Query(`SELECT id, name, executor, version, last_seen, pool, targets, features, kind, auth, labels, accept, os, arch
		FROM runners ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	online := e.now().Add(-RunnerOffline).UnixMilli()
	out := []RunnerView{}
	at := map[string]int{}
	for rows.Next() {
		var r RunnerView
		var targets, features, labels, acc string
		if err := rows.Scan(&r.ID, &r.Name, &r.Executor, &r.Version, &r.LastSeen, &r.Pool, &targets, &features, &r.Kind, &r.Auth,
			&labels, &acc, &r.OS, &r.Arch); err != nil {
			rows.Close()
			return nil, err
		}
		r.Images = slices.Contains(strings.Fields(features), FeatureImages)
		if targets != "" {
			r.Targets = strings.Split(targets, ",")
		}
		if r.Kind == "" { // written by an older server
			r.Kind = PoolBuild
			if r.Pool != "" {
				r.Kind, r.Auth = PoolDeploy, AuthPoolToken
			}
		}
		if r.Auth == "" {
			r.Auth = AuthShared
		}
		if r.Kind == PoolBuild && r.Pool == "" {
			r.Pool = DefaultPool
		}
		r.Labels, r.Accept = splitLabels(labels), decodeAccept(acc)
		r.Online = r.LastSeen >= online
		r.Work = []RunnerWork{}
		at[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idents, err := e.RunnerIdentities()
	if err != nil {
		return nil, err
	}
	for _, id := range idents {
		i, ok := at[id.ID]
		if !ok {
			// Enrolled, but not asking for work: it has not started, it
			// was revoked, or it has been gone for a day.
			i = len(out)
			at[id.ID] = i
			out = append(out, RunnerView{ID: id.ID, Name: id.Name, Executor: id.Executor, Version: id.Version, LastSeen: id.LastSeen,
				Pool: id.Pool, Kind: id.Kind, Auth: AuthIdentity, Labels: id.Labels, OS: id.OS, Arch: id.Arch, Work: []RunnerWork{},
				Online: id.RevokedAt == 0 && id.LastSeen >= online})
		}
		out[i].Identity = id
		if out[i].Pool == "" {
			out[i].Pool = id.Pool
		}
	}
	work, err := e.db.Query(`SELECT l.runner, l.run_id, runs.project, runs.number, l.step_id FROM leases l JOIN runs ON runs.id = l.run_id
		ORDER BY l.run_id, l.step_id`)
	if err != nil {
		return nil, err
	}
	defer work.Close()
	for work.Next() {
		var runner string
		var w RunnerWork
		if err := work.Scan(&runner, &w.Run, &w.Project, &w.Number, &w.Step); err != nil {
			return nil, err
		}
		if i, ok := at[runner]; ok {
			out[i].Work = append(out[i].Work, w)
		}
	}
	return out, work.Err()
}
