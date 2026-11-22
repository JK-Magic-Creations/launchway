package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// RewriteTokenRepos lets fn change the repository list of every token of the
// Base that has one (a repository was renamed or deleted); fn returns the new
// list and whether it changed.
func (i *Identity) RewriteTokenRepos(sc Scope, fn func([]TokenRepo) ([]TokenRepo, bool)) error {
	return i.env.Do(sc, true, func(q *Q) error {
		todo, err := tokenRepoUpdates(q, fn)
		if err != nil {
			return err
		}
		for _, u := range todo {
			if _, err := q.Exec(`UPDATE api_tokens SET repos = ? WHERE base_id = ? AND id = ?`, u.repos, q.Base(), u.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- service accounts ----

// CreateServiceAccount adds a service account; ErrConflict if the name is taken.
func (i *Identity) CreateServiceAccount(sc Scope, a ServiceAccount) error {
	err := i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`INSERT INTO service_accounts(base_id, name, description, role, disabled, created_at, created_by) VALUES(?,?,?,?,0,?,?)`,
			q.Base(), a.Name, a.Description, a.Role, a.CreatedAt, a.CreatedBy)
		return err
	})
	if IsConflict(err) {
		return fmt.Errorf("%w: a service account called %q exists", ErrConflict, a.Name)
	}
	return err
}

// ServiceAccounts lists the service accounts, by name.
func (i *Identity) ServiceAccounts(sc Scope) ([]ServiceAccount, error) {
	out := []ServiceAccount{}
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT name, description, role, disabled, created_at, created_by FROM service_accounts WHERE base_id = ? ORDER BY name`, q.Base())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a ServiceAccount
			var dis int
			if err := rows.Scan(&a.Name, &a.Description, &a.Role, &dis, &a.CreatedAt, &a.CreatedBy); err != nil {
				return err
			}
			a.Disabled = dis != 0
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// ServiceAccount finds one by name.
func (i *Identity) ServiceAccount(sc Scope, name string) (*ServiceAccount, error) {
	var a ServiceAccount
	var dis int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT name, description, role, disabled, created_at, created_by FROM service_accounts WHERE base_id = ? AND name = ?`, q.Base(), name).
			Scan(&a.Name, &a.Description, &a.Role, &dis, &a.CreatedAt, &a.CreatedBy)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("service account " + name)
	}
	a.Disabled = dis != 0
	return &a, err
}

// SetServiceAccount changes a service account's role and whether it is off.
func (i *Identity) SetServiceAccount(sc Scope, name string, role *string, disabled *bool) error {
	return i.env.Do(sc, true, func(q *Q) error {
		var found int64
		if role != nil {
			res, err := q.Exec(`UPDATE service_accounts SET role = ? WHERE base_id = ? AND name = ?`, *role, q.Base(), name)
			if err != nil {
				return err
			}
			found, _ = res.RowsAffected()
		}
		if disabled != nil {
			d := 0
			if *disabled {
				d = 1
			}
			res, err := q.Exec(`UPDATE service_accounts SET disabled = ? WHERE base_id = ? AND name = ?`, d, q.Base(), name)
			if err != nil {
				return err
			}
			found, _ = res.RowsAffected()
		}
		if found == 0 {
			return notFound("service account " + name)
		}
		return nil
	})
}

// DeleteServiceAccount removes the account and every token it owns.
func (i *Identity) DeleteServiceAccount(sc Scope, name, principal string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`DELETE FROM service_accounts WHERE base_id = ? AND name = ?`, q.Base(), name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("service account " + name)
		}
		_, err = q.Exec(`DELETE FROM api_tokens WHERE base_id = ? AND username = ?`, q.Base(), principal)
		return err
	})
}

func anys(l []string) []any {
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = v
	}
	return out
}

type repoUpdate struct{ id, repos string }

// tokenRepoUpdates reads the tokens that have repositories and what fn would make of each list.
func tokenRepoUpdates(q *Q, fn func([]TokenRepo) ([]TokenRepo, bool)) ([]repoUpdate, error) {
	rows, err := q.Query(`SELECT id, repos FROM api_tokens WHERE base_id = ? AND repos <> ''`, q.Base())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todo []repoUpdate
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var list []TokenRepo
		if json.Unmarshal([]byte(raw), &list) != nil {
			continue
		}
		if out, changed := fn(list); changed {
			todo = append(todo, repoUpdate{id, encodeList(out)})
		}
	}
	return todo, rows.Err()
}
