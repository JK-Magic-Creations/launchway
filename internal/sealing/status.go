package sealing

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// ColumnReport counts one registered column's values.
type ColumnReport struct {
	Table     string `json:"table"`
	Column    string `json:"column"`
	Why       string `json:"what"`
	Sealed    int    `json:"sealed"`
	Remaining int    `json:"remaining"` // stored in the clear
}

// Report is the installation's encryption at rest as the doctor, the API and
// the settings page show it.
type Report struct {
	// Encrypted says a root key is in use.
	Encrypted bool `json:"encrypted"`
	// DumpAloneIsUseless: a copy of the database alone cannot reveal any
	// secret this build seals (a root key is in use, and nothing is left in
	// the clear). It says nothing about artifacts, logs, caches and Git
	// packs, which are the next slice (see NotYetCovered).
	DumpAloneIsUseless bool                  `json:"dump_alone_cannot_reveal_secrets"`
	Holder             *keyring.HolderStatus `json:"root_key_holder,omitempty"`
	Keys               *keyring.Status       `json:"keys,omitempty"`
	Columns            []ColumnReport        `json:"columns"`
	Sealed             int                   `json:"secrets_sealed"`
	Remaining          int                   `json:"secrets_remaining"`
	Migration          Migration             `json:"migration"`
	CredentialAddress  int                   `json:"stored_addresses_with_credentials"`
	Warnings           []string              `json:"warnings"`
	Advice             []string              `json:"advice"`
	NotYetCovered      []string              `json:"not_yet_encrypted"`
	// ConfiguredHolder is set when a root key is configured but this
	// database has not been sealed with it yet (the next start of a server
	// does that).
	ConfiguredHolder string `json:"configured_but_unused_holder,omitempty"`
	// Summary is the status in one plain sentence.
	Summary string `json:"summary"`
}

// Migration is the progress of sealing what was stored in the clear.
type Migration struct {
	State       string `json:"state"` // not started, running, complete, needs attention
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	Running     string `json:"running_on,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	// Waiting is set while sealing of what already existed waits for servers
	// of an older release to stop; OlderServers is how many were seen at the
	// last look.
	Waiting      bool `json:"waiting_for_older_servers,omitempty"`
	OlderServers int  `json:"older_servers_seen,omitempty"`
}

// NotYetEncrypted is what this slice does not cover. It is said wherever
// encryption is reported.
var NotYetEncrypted = []string{
	"artifacts, test attachments and reports",
	"logs",
	"build caches",
	"hosted Git repository packs",
	"registry images, charts and other OCI artifacts",
}

// Report reads the state. It changes nothing.
func (s *Service) Report(ctx context.Context) (Report, error) {
	r := Report{Encrypted: s.Encrypted(), NotYetCovered: NotYetEncrypted, Warnings: []string{}, Advice: []string{}}
	for _, c := range Registry {
		cr := ColumnReport{Table: c.Table, Column: c.Column, Why: c.Why}
		where := ""
		if c.Where != "" {
			where = " AND (" + c.Where + ")"
		}
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s <> ''%s`, c.Table, c.Column, where)
		var total int
		if err := s.db.QueryRow(ctx, q).Scan(&total); err != nil {
			if isMissingTable(err) {
				r.Columns = append(r.Columns, cr)
				continue
			}
			return r, err
		}
		if err := s.db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s LIKE ?%s`, c.Table, c.Column, where), keyring.Prefix+"%").Scan(&cr.Sealed); err != nil {
			return r, err
		}
		cr.Remaining = total - cr.Sealed
		r.Sealed += cr.Sealed
		r.Remaining += cr.Remaining
		r.Columns = append(r.Columns, cr)
	}
	if s.kr != nil {
		ks, err := s.kr.Status(ctx)
		if err != nil {
			return r, err
		}
		r.Keys, r.Holder = &ks, &ks.Holder
	}
	if s.configured != "" && !s.Encrypted() {
		r.ConfiguredHolder = s.configured
	}
	r.Migration = s.migration(ctx, r.Remaining)
	r.CredentialAddress, _ = s.countCredentialAddresses(ctx)
	r.DumpAloneIsUseless = r.Encrypted && r.Remaining == 0 && r.Migration.State == "complete"
	s.advise(&r)
	return r, nil
}

func (s *Service) migration(ctx context.Context, remaining int) Migration {
	var m Migration
	if v, ok := s.state(ctx, "migration_started_at"); ok {
		m.StartedAt, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := s.state(ctx, "migration_completed_at"); ok {
		m.CompletedAt, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := s.state(ctx, "migration_last_error"); ok {
		m.LastError = v
	}
	if v, ok := s.state(ctx, "migration_lease"); ok {
		owner, exp, _ := strings.Cut(v, "|")
		if ms, err := strconv.ParseInt(exp, 10, 64); err == nil && ms > s.now().UnixMilli() {
			m.Running = owner
		}
	}
	if n, _, ok := s.waiting(ctx); ok && remaining > 0 {
		m.Waiting, m.OlderServers = true, n
	}
	switch {
	case !s.Encrypted():
		m.State = "not started"
	case m.Waiting:
		m.State = "waiting for older servers"
	case m.LastError != "":
		m.State = "needs attention"
	case m.Running != "":
		m.State = "running"
	case remaining == 0 && s.complete(ctx):
		m.State = "complete"
	case remaining == 0:
		m.State = "complete"
	default:
		m.State = "not started"
		if m.StartedAt != 0 {
			m.State = "running"
		}
	}
	return m
}

func (s *Service) advise(r *Report) {
	if !r.Encrypted && r.ConfiguredHolder != "" {
		r.Summary = fmt.Sprintf("A %s root key is configured, but no server has started with it yet, so %d stored secrets are still in the clear. Start (or restart) a server: it seals them in place.", r.ConfiguredHolder, r.Remaining)
		r.Warnings = append(r.Warnings, r.Summary)
	} else if !r.Encrypted {
		r.Summary = "NOT ENCRYPTED AT REST: this server has no root key, so its signing keys, the shared runner token" +
			" and (if used) SSH host keys and webhook secrets are stored in the clear in the database. A copy of the database reveals them."
		r.Warnings = append(r.Warnings, r.Summary, "encryption is not set up: no root key is configured; configure one and restart: "+keyring.HowToCreateARootKey)
		r.Advice = append(r.Advice, "Until a root key exists, protect the database, its backups and its volume as you would the secrets themselves.")
	} else {
		switch {
		case r.Remaining > 0 && r.Migration.Waiting:
			r.Summary = fmt.Sprintf("Encryption at rest is on. %d stored secrets from before are still in the clear on purpose: sealing waits until no server of the previous release is running (%d seen at the last look), so that none of them reads a sealed value during the rolling update. It then seals them by itself.", r.Remaining, r.Migration.OlderServers)
			r.Warnings = append(r.Warnings, r.Summary)
		case r.Remaining > 0:
			r.Summary = fmt.Sprintf("Encryption at rest is on, but %d stored secrets are still in the clear (sealing is %s).", r.Remaining, r.Migration.State)
			r.Warnings = append(r.Warnings, r.Summary)
		default:
			r.Summary = fmt.Sprintf("Encryption at rest is on: %d stored secrets are sealed and none remain in the clear. A copy of the database alone cannot reveal them; it needs the root key (%s).", r.Sealed, r.Holder.Kind)
		}
		if r.Keys != nil && r.Keys.NeedsRewrap {
			r.Warnings = append(r.Warnings, "A previous root key is still needed to open the keys: run `launchway encryption rewrap`, then remove the previous key from the configuration.")
		}
		if r.Holder != nil && !r.Holder.Reachable {
			r.Warnings = append(r.Warnings, "The root key holder cannot be reached: "+r.Holder.Problem)
		}
		if r.Migration.LastError != "" {
			r.Warnings = append(r.Warnings, "Sealing the stored secrets stopped: "+r.Migration.LastError)
		}
		if r.Holder != nil {
			r.Advice = append(r.Advice, r.Holder.Strength)
		}
		if r.Keys != nil && r.Keys.OldestKeyMillis > 0 {
			if age := time.Since(time.UnixMilli(r.Keys.OldestKeyMillis)); age > 365*24*time.Hour {
				r.Warnings = append(r.Warnings, fmt.Sprintf("The oldest key-encryption key is %d days old (the yearly policy is exceeded): run `launchway encryption rotate`.", int(age.Hours()/24)))
			}
		}
		r.Advice = append(r.Advice, "Keep a copy of the root key somewhere other than the database's backups: without it a restore cannot read any secret, and nobody can recover it for you.")
	}
	r.Warnings = append(r.Warnings, s.notes...)
	if r.CredentialAddress > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d stored addresses (mirrors, webhooks, connections) carry a user name or password in the address; edit them and give the credential as a reference.", r.CredentialAddress))
	}
	r.Advice = append(r.Advice,
		"Not yet encrypted by Launchway: "+strings.Join(r.NotYetCovered, ", ")+". Until that is done, use volume or database encryption for the database and the artifact store.",
		"Disk or volume encryption cannot be seen from here: unknown.")
}

// countCredentialAddresses counts stored addresses that carry a user name or
// password. Every write path refuses them; this finds any that were stored
// before, so they can be edited.
func (s *Service) countCredentialAddresses(ctx context.Context) (int, error) {
	n := 0
	for _, c := range []struct{ table, col string }{{"repo_mirrors", "url"}, {"repo_webhooks", "url"}, {"forge_connections", "base_url"}} {
		err := eachRow(ctx, s.db, fmt.Sprintf(`SELECT %s FROM %s WHERE %s LIKE '%%://%%@%%'`, c.col, c.table, c.col), nil, func(rs *sql.Rows) error {
			var a string
			if err := rs.Scan(&a); err != nil {
				return err
			}
			if u, err := url.Parse(a); err == nil && u.User != nil {
				if _, hasPass := u.User.Password(); hasPass || u.Scheme == "http" || u.Scheme == "https" {
					n++
				}
			}
			return nil
		})
		if err != nil && !isMissingTable(err) {
			return n, err
		}
	}
	return n, nil
}

// IsFresh says whether the database has never held a secret this package
// seals: a brand-new installation, which gets a root key by default.
func IsFresh(ctx context.Context, db DB) (bool, error) {
	for _, c := range Registry {
		where := ""
		if c.Where != "" {
			where = " WHERE " + c.Where
		}
		var n int
		err := db.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s%s`, c.Table, where)).Scan(&n)
		if err != nil && err != sql.ErrNoRows {
			if isMissingTable(err) {
				continue
			}
			return false, err
		}
		if n > 0 {
			return false, nil
		}
	}
	var k int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&k); err == nil && k > 0 {
		return false, nil
	}
	return true, nil
}

// earlierDataTables are tables a running installation fills and a database
// created a moment ago has empty. Any row in one of them means the database
// is an earlier installation being upgraded, not a new one.
var earlierDataTables = []string{"users", "local_accounts", "audit", "runs", "runners", "config_objects", "deployments",
	"forge_connections", "sessions", "api_tokens", "events", "repo_refs", "run_queue", "variables"}

// IsBrandNew says whether the database is one created for this very start:
// it has never held a secret (IsFresh), no row in any table an installation in
// use fills, and nothing in meta beyond the schema markers and the liveness
// stamp. Only such a database may get a root key generated for it, and only
// when it is a local SQLite file (see cmd/launchway startSecrets): a database
// that holds earlier data is an upgrade, and a key invented for it would
// surprise the installation.
func IsBrandNew(ctx context.Context, db DB) (bool, error) {
	if fresh, err := IsFresh(ctx, db); err != nil || !fresh {
		return false, err
	}
	for _, t := range earlierDataTables {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			if isMissingTable(err) {
				continue
			}
			return false, err
		}
		if n > 0 {
			return false, nil
		}
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM meta WHERE key NOT IN ('alive_at','schema_version','schema_min','artifact_store_marker')`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}
