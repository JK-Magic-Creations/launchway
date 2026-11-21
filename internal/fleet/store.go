package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Store keeps the inventory of an Airfield's fleet (schema 49).
type Store struct {
	DB  *engine.Database
	Now func() time.Time
}

// MaxHosts bounds one fleet.
const MaxHosts = 500

// SetHosts replaces the Airfield's fleet. Every host is validated first
// (a host with no pinned key is refused) and names are unique.
func (s *Store) SetHosts(ctx context.Context, env string, hosts []Host) error {
	if !nameRe.MatchString(env) {
		return fmt.Errorf("airfield %q: use lower-case letters, digits and hyphens", env)
	}
	if len(hosts) > MaxHosts {
		return fmt.Errorf("a fleet holds at most %d hosts", MaxHosts)
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if err := h.Validate(); err != nil {
			return err
		}
		if seen[h.Name] {
			return fmt.Errorf("host %s is listed twice", h.Name)
		}
		seen[h.Name] = true
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(ctx, `DELETE FROM fleet_hosts WHERE environment = ?`, env); err != nil {
		return err
	}
	for _, h := range hosts {
		user := h.User
		if user == "" {
			user = "deploy"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO fleet_hosts(environment, name, address, ssh_user, host_key, service, member, group_name, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, env, h.Name, h.Address, user, h.HostKey, h.Service, h.Member, h.Group, now.UnixMilli(), now.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Hosts returns the Airfield's fleet, in name order.
func (s *Store) Hosts(ctx context.Context, env string) ([]Host, error) {
	rows, err := s.DB.Query(ctx, `SELECT name, address, ssh_user, host_key, service, member, group_name FROM fleet_hosts WHERE environment = ? ORDER BY name`, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.Name, &h.Address, &h.User, &h.HostKey, &h.Service, &h.Member, &h.Group); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
