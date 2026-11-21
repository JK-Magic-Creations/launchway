package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"time"
)

// DatabaseState is what the server can tell about the database it uses:
// whether it can be written (not only reached), whether it is a standby, and
// what replication and archiving it can see.
type DatabaseState struct {
	Kind     string `json:"kind"` // postgresql or sqlite
	Version  string `json:"version,omitempty"`
	Writable bool   `json:"writable"`
	Standby  bool   `json:"standby,omitempty"` // in recovery: a replica that follows a primary
	// Replication is set on PostgreSQL when it can be seen: on a primary
	// from pg_stat_replication, on a standby from the WAL receiver. It is
	// absent when the database user may not see it (see ReplicationVisible).
	Replication        *ReplicationState `json:"replication,omitempty"`
	ReplicationVisible bool              `json:"replication_visible"`
	// Archiving is PostgreSQL's WAL archiving, when it is visible.
	Archiving *ArchivingState `json:"archiving,omitempty"`
	File      string          `json:"file,omitempty"` // SQLite: the database file
}

// ReplicationState is the replication a PostgreSQL database reports.
type ReplicationState struct {
	Standbys     int    `json:"standbys"`      // streaming standbys seen from a primary
	SyncStandbys int    `json:"sync_standbys"` // of them, synchronous or quorum members
	MaxLagMillis *int64 `json:"max_lag_ms,omitempty"`
	Receiver     string `json:"wal_receiver,omitempty"`  // on a standby: streaming, or what it is doing
	ReplayLagMs  *int64 `json:"replay_lag_ms,omitempty"` // on a standby
	SyncNames    string `json:"synchronous_standby_names,omitempty"`
}

// ArchivingState is whether PostgreSQL ships its write-ahead log elsewhere,
// which is what allows recovery to a point in time.
type ArchivingState struct {
	Enabled         bool   `json:"enabled"`
	LastArchivedAt  string `json:"last_archived_at,omitempty"`
	LastFailedAt    string `json:"last_failed_at,omitempty"`
	LastArchivedAgo *int64 `json:"last_archived_ago_s,omitempty"`
}

// DatabaseState reads the state of the database this engine uses.
func (e *Engine) DatabaseState(ctx context.Context) (DatabaseState, error) {
	return e.db.state(ctx)
}

// InspectDatabase opens the database at addr without starting an engine and
// reads its state, for `launchway doctor`.
func InspectDatabase(ctx context.Context, addr string, wait time.Duration) (DatabaseState, error) {
	var d *dbx
	if isPostgres(addr) {
		db, err := sql.Open("pgx", addr)
		if err != nil {
			return DatabaseState{}, err
		}
		db.SetMaxOpenConns(2)
		d = &dbx{DB: db, pg: true, dsn: addr}
	} else {
		q := url.Values{}
		q.Add("_pragma", "busy_timeout(10000)")
		db, err := sql.Open("sqlite", "file:"+addr+"?"+q.Encode())
		if err != nil {
			return DatabaseState{}, err
		}
		db.SetMaxOpenConns(1)
		d = &dbx{DB: db}
	}
	defer d.Close()
	if err := connect(d, wait, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		return DatabaseState{}, fmt.Errorf("connecting to the database: %w", err)
	}
	st, err := d.state(ctx)
	if err == nil && !d.pg {
		st.File = addr
	}
	return st, err
}

func (d *dbx) state(ctx context.Context) (DatabaseState, error) {
	if !d.pg {
		var v string
		if err := d.DB.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&v); err != nil {
			return DatabaseState{}, err
		}
		st := DatabaseState{Kind: "sqlite", Version: v, Writable: true}
		var ro int
		if err := d.DB.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&ro); err == nil && ro != 0 {
			st.Writable = false
		}
		return st, nil
	}
	st := DatabaseState{Kind: "postgresql"}
	var readOnly string
	var recovery bool
	if err := d.DB.QueryRowContext(ctx, `SELECT current_setting('server_version'), pg_is_in_recovery(), current_setting('transaction_read_only')`).
		Scan(&st.Version, &recovery, &readOnly); err != nil {
		return st, err
	}
	// Writable means a write would be accepted: not a standby, and not set
	// read-only (a replica attached to a read-only database must not take
	// requests).
	st.Standby = recovery
	st.Writable = !recovery && readOnly != "on"
	// What may be seen depends on the database user's rights; everything
	// below is best effort and never makes the state an error.
	var visible bool
	if err := d.DB.QueryRowContext(ctx, `SELECT pg_has_role(current_user, 'pg_read_all_stats', 'member')`).Scan(&visible); err == nil {
		st.ReplicationVisible = visible
	}
	if st.ReplicationVisible {
		if recovery {
			st.Replication = d.standbyState(ctx)
		} else {
			st.Replication = d.primaryState(ctx)
		}
	}
	st.Archiving = d.archivingState(ctx)
	return st, nil
}

func (d *dbx) primaryState(ctx context.Context) *ReplicationState {
	r := &ReplicationState{}
	var lag sql.NullInt64
	if err := d.DB.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE state = 'streaming'),
			count(*) FILTER (WHERE state = 'streaming' AND sync_state IN ('sync', 'quorum')),
			max((extract(epoch FROM replay_lag) * 1000)::bigint)
		FROM pg_stat_replication`).Scan(&r.Standbys, &r.SyncStandbys, &lag); err != nil {
		return nil
	}
	if lag.Valid {
		r.MaxLagMillis = &lag.Int64
	}
	_ = d.DB.QueryRowContext(ctx, `SELECT current_setting('synchronous_standby_names')`).Scan(&r.SyncNames)
	return r
}

func (d *dbx) standbyState(ctx context.Context) *ReplicationState {
	r := &ReplicationState{}
	var status sql.NullString
	var lag sql.NullInt64
	_ = d.DB.QueryRowContext(ctx, `SELECT status FROM pg_stat_wal_receiver LIMIT 1`).Scan(&status)
	_ = d.DB.QueryRowContext(ctx, `SELECT (extract(epoch FROM now() - pg_last_xact_replay_timestamp()) * 1000)::bigint`).Scan(&lag)
	r.Receiver = status.String
	if lag.Valid {
		r.ReplayLagMs = &lag.Int64
	}
	return r
}

func (d *dbx) archivingState(ctx context.Context) *ArchivingState {
	var mode string
	if err := d.DB.QueryRowContext(ctx, `SELECT current_setting('archive_mode')`).Scan(&mode); err != nil {
		return nil
	}
	a := &ArchivingState{Enabled: mode == "on" || mode == "always"}
	var last, failed sql.NullTime
	var ago sql.NullInt64
	if err := d.DB.QueryRowContext(ctx, `SELECT last_archived_time, last_failed_time,
			(extract(epoch FROM now() - last_archived_time))::bigint FROM pg_stat_archiver`).Scan(&last, &failed, &ago); err == nil {
		if last.Valid {
			a.LastArchivedAt = last.Time.UTC().Format(time.RFC3339)
			a.LastArchivedAgo = &ago.Int64
		}
		if failed.Valid {
			a.LastFailedAt = failed.Time.UTC().Format(time.RFC3339)
		}
	}
	return a
}

// Assessment says plainly whether an installation would survive the loss of
// its database, and what would be lost.
type Assessment struct {
	// Level is "highly available", "not highly available", "at risk" or
	// "unknown".
	Level string   `json:"level"`
	Lines []string `json:"lines"`
}

// Assess says what the state means for the installation.
func (s DatabaseState) Assess() Assessment {
	var a Assessment
	say := func(f string, args ...any) { a.Lines = append(a.Lines, fmt.Sprintf(f, args...)) }
	switch {
	case s.Kind == "sqlite":
		a.Level = "not highly available"
		say("Database: SQLite file %s.", nonEmpty(s.File, "(embedded)"))
		say("Not highly available: a failure of this machine or its disk stops Launchway until the file is restored.")
		if st, err := os.Stat(s.File); err == nil && s.File != "" {
			say("The file was last written %s.", ago(time.Since(st.ModTime())))
		}
		say("If the file is lost, everything since the last backup is lost; Launchway cannot see backups of the file, so the last backup is not known to it.")
	case s.Standby:
		a.Level = "at risk"
		say("Database: PostgreSQL %s, a standby (read-only). Launchway cannot write to it: point it at the primary.", s.Version)
	case !s.Writable:
		a.Level = "at risk"
		say("Database: PostgreSQL %s, set read-only. Launchway cannot write to it.", s.Version)
	case !s.ReplicationVisible:
		a.Level = "unknown"
		say("Database: PostgreSQL %s, writable.", s.Version)
		say("Replication is not visible to this database user (it needs the pg_monitor or pg_read_all_stats role), so whether a standby exists cannot be verified here.")
		s.archiving(say)
	case s.Replication == nil || s.Replication.Standbys == 0:
		a.Level = "not highly available"
		say("Database: PostgreSQL %s, writable, with no standby streaming from it.", s.Version)
		say("Not highly available: a database failure stops Launchway until it is restored.")
		s.archiving(say)
	case s.Replication.SyncStandbys == 0:
		a.Level = "at risk"
		say("Database: PostgreSQL %s, writable, with %d standby(s) streaming, none of them synchronous.", s.Version, s.Replication.Standbys)
		say("If the primary is lost, the last changes it had not yet sent to a standby are lost, and a failover manager is needed to promote one.")
		s.archiving(say)
	default:
		a.Level = "highly available"
		say("Database: PostgreSQL %s, writable, with %d standby(s) streaming, %d synchronous%s.", s.Version, s.Replication.Standbys, s.Replication.SyncStandbys, lagText(s.Replication.MaxLagMillis))
		say("If the primary is lost, a confirmed change is on a standby; Launchway waits for the promotion (a failover manager does it; this check cannot see one) and carries on.")
		s.archiving(say)
	}
	return a
}

func (s DatabaseState) archiving(say func(string, ...any)) {
	switch ar := s.Archiving; {
	case ar == nil:
		say("Backups are not visible to Launchway; the last backup is not known to it.")
	case !ar.Enabled:
		say("WAL archiving is off: no point-in-time recovery; the last backup is not known to Launchway.")
	case ar.LastArchivedAgo != nil:
		say("WAL archiving is on; the last segment was archived %s (the last base backup is not known to Launchway).", ago(time.Duration(*ar.LastArchivedAgo)*time.Second))
	default:
		say("WAL archiving is on, but nothing has been archived yet.")
	}
	if s.Archiving != nil && s.Archiving.LastFailedAt != "" {
		say("The last failed archive attempt was at %s.", s.Archiving.LastFailedAt)
	}
}

func lagText(ms *int64) string {
	if ms == nil {
		return ""
	}
	return fmt.Sprintf(", lag %d ms", *ms)
}

func ago(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d seconds ago", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
