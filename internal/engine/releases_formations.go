package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// A Formation groups releases of several projects that are promoted together,
// in an explicit order (the position of each member). Today the order is
// written by the person who makes the formation; when the dependency graph of
// the projects exists it will propose it. A formation reports all or nothing:
// no member is started unless every member's checklist passes for the
// Airfield, and when one member's Sortie fails the rest are not started and
// the report says which launched, which failed and which were not started.

// Formation states.
const (
	FormationPlanned   = "planned"
	FormationPromoting = "promoting"
	FormationDone      = "done"
	FormationPartial   = "partial" // some members launched, then one failed
	FormationFailed    = "failed"  // the first member failed, or none could start
)

// Member states.
const (
	MemberWaiting    = "waiting"
	MemberStarted    = "started"
	MemberLaunched   = "launched"
	MemberFailed     = "failed"
	MemberNotStarted = "not_started"
)

var formationNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,63}$`)

// FormationMember is one release in a formation.
type FormationMember struct {
	ReleaseID   string `json:"release_id"`
	Project     string `json:"project"`
	Version     string `json:"version"`
	Position    int    `json:"position"`
	State       string `json:"state"`
	PromotionID string `json:"promotion_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Formation is a group of releases promoted together.
type Formation struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Note      string            `json:"note,omitempty"`
	State     string            `json:"state"`
	Airfield  string            `json:"airfield,omitempty"`
	CreatedBy string            `json:"created_by,omitempty"`
	CreatedAt int64             `json:"created_at"`
	ClosedAt  int64             `json:"closed_at,omitempty"`
	Members   []FormationMember `json:"members"`
	// OrderSource says where the order came from: "explicit" today.
	OrderSource string `json:"order_source"`
}

// CreateFormation groups releases, in the order given.
func (e *Engine) CreateFormation(name, note string, releaseIDs []string, actor string) (*Formation, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	if !formationNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: a formation's name is letters, digits, spaces and . _ - , up to 64", ErrInvalid)
	}
	if len(releaseIDs) < 2 || len(releaseIDs) > 50 {
		return nil, fmt.Errorf("%w: a formation groups 2 to 50 releases", ErrInvalid)
	}
	f := &Formation{ID: ids.New("frm"), Name: name, Note: clipText(note, 500), State: FormationPlanned, CreatedBy: actor, CreatedAt: e.now().UnixMilli(), OrderSource: "explicit"}
	seen := map[string]bool{}
	for i, id := range releaseIDs {
		r, err := e.GetRelease(id)
		if err != nil {
			return nil, err
		}
		if r.State != ReleasePublished {
			return nil, fmt.Errorf("%w: %s %s is %s; a formation groups published releases", ErrConflict, r.Project, r.Version, r.State)
		}
		if seen[r.Project] {
			return nil, fmt.Errorf("%w: the formation has two releases of %s", ErrInvalid, r.Project)
		}
		seen[r.Project] = true
		f.Members = append(f.Members, FormationMember{ReleaseID: r.ID, Project: r.Project, Version: r.Version, Position: i, State: MemberWaiting})
	}
	err := e.db.retryTx(maxAttempts, false, func() error {
		unlock := e.writeLock()
		defer unlock()
		tx, err := e.db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`INSERT INTO release_formations(id, name, note, state, created_by, created_at) VALUES(?,?,?,?,?,?)`, f.ID, f.Name, f.Note, f.State, actor, f.CreatedAt); err != nil {
			return err
		}
		for _, m := range f.Members {
			if _, err := tx.Exec(`INSERT INTO release_formation_members(formation_id, release_id, position, state) VALUES(?,?,?,?)`, f.ID, m.ReleaseID, m.Position, m.State); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: there is already a formation called %s", ErrConflict, name)
		}
		return nil, err
	}
	return f, nil
}

// GetFormation reads a formation with its members.
func (e *Engine) GetFormation(id string) (*Formation, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	f := &Formation{OrderSource: "explicit"}
	err := e.db.QueryRow(`SELECT id, name, note, state, airfield, created_by, created_at, closed_at FROM release_formations WHERE id = ? OR name = ?`, id, id).
		Scan(&f.ID, &f.Name, &f.Note, &f.State, &f.Airfield, &f.CreatedBy, &f.CreatedAt, &f.ClosedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: formation %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT m.release_id, r.project, r.version, m.position, m.state, m.promotion_id, m.detail FROM release_formation_members m
		JOIN releases r ON r.id = m.release_id WHERE m.formation_id = ? ORDER BY m.position`, f.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	f.Members = []FormationMember{}
	for rows.Next() {
		var m FormationMember
		if err := rows.Scan(&m.ReleaseID, &m.Project, &m.Version, &m.Position, &m.State, &m.PromotionID, &m.Detail); err != nil {
			return nil, err
		}
		f.Members = append(f.Members, m)
	}
	return f, rows.Err()
}

// ListFormations lists formations, newest first.
func (e *Engine) ListFormations(limit int) ([]Formation, error) {
	if err := e.needReleases(); err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT id FROM release_formations ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var idl []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			idl = append(idl, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Formation{}
	for _, id := range idl {
		f, err := e.GetFormation(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// SetFormation records the formation's state and Airfield.
func (e *Engine) SetFormation(id, state, airfield string) error {
	closed := int64(0)
	if state == FormationDone || state == FormationPartial || state == FormationFailed {
		closed = e.now().UnixMilli()
	}
	_, err := e.db.Exec(`UPDATE release_formations SET state = ?, airfield = CASE WHEN ? <> '' THEN ? ELSE airfield END, closed_at = ? WHERE id = ?`, state, airfield, airfield, closed, id)
	return err
}

// SetFormationMember records a member's state.
func (e *Engine) SetFormationMember(formationID, releaseID, state, promotionID, detail string) error {
	_, err := e.db.Exec(`UPDATE release_formation_members SET state = ?, promotion_id = CASE WHEN ? <> '' THEN ? ELSE promotion_id END, detail = ? WHERE formation_id = ? AND release_id = ?`,
		state, promotionID, promotionID, clipText(detail, 500), formationID, releaseID)
	return err
}

// Promotion finds a promotion by id.
func (e *Engine) Promotion(id string) (*ReleasePromotion, error) {
	p, err := scanPromotion(e.db.QueryRow(`SELECT `+promoCols+` FROM release_promotions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: promotion %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	p.State, p.Detail = e.promotionOutcome(p)
	return &p, nil
}

// FormationProjects returns the projects of a formation's members.
func (f *Formation) FormationProjects() []string {
	var out []string
	for _, m := range f.Members {
		out = append(out, m.Project)
	}
	return out
}

// Summary is one line saying how a formation stands.
func (f *Formation) Summary() string {
	n := map[string]int{}
	for _, m := range f.Members {
		n[m.State]++
	}
	var parts []string
	for _, s := range []string{MemberLaunched, MemberStarted, MemberFailed, MemberNotStarted, MemberWaiting} {
		if n[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[s], strings.ReplaceAll(s, "_", " ")))
		}
	}
	return strings.Join(parts, ", ")
}
