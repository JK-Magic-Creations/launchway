package notices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// ErrInvalid is returned for a choice that is not one.
var ErrInvalid = errors.New("invalid choice")

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...)
}

// Notice is one line of a person's inbox.
type Notice struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Repository string    `json:"repository,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail,omitempty"`
	Link       string    `json:"link,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	Restricted bool      `json:"restricted,omitempty"`
	At         time.Time `json:"at"`
	Read       bool      `json:"read"`
	Done       bool      `json:"done"`
	mailState  string
}

// Store is the notices' tables.
type Store struct {
	DB  DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const noticeCols = `id, kind, repo, subject, title, detail, link, actor, restricted, mail_state, created_at, read_at, done_at`

func scanNotice(r interface{ Scan(...any) error }) (Notice, error) {
	var n Notice
	var restricted int
	var at, read, done int64
	if err := r.Scan(&n.ID, &n.Kind, &n.Repository, &n.Subject, &n.Title, &n.Detail, &n.Link, &n.Actor, &restricted, &n.mailState, &at, &read, &done); err != nil {
		return n, err
	}
	n.Restricted, n.At, n.Read, n.Done = restricted != 0, time.UnixMilli(at).UTC(), read != 0, done != 0
	return n, nil
}

// Add stores a notice for a person.
func (s *Store) Add(ctx context.Context, person string, n Notice, mailState, dedupe string) (Notice, error) {
	n.ID = ids.New("ntc")
	r := 0
	if n.Restricted {
		r = 1
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notices(`+noticeCols[:strings.LastIndex(noticeCols, ", created_at")]+`, recipient, dedupe, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Kind, n.Repository, n.Subject, clip(n.Title, 300), clip(n.Detail, 1000), n.Link, n.Actor, r, mailState, person, dedupe, s.now().UnixMilli())
	if err != nil {
		return n, err
	}
	n.At = s.now().UTC()
	n.mailState = mailState
	return n, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Seen says whether a person already has a notice with this dedupe key.
func (s *Store) Seen(ctx context.Context, person, dedupe string) bool {
	if dedupe == "" {
		return false
	}
	var n int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND dedupe = ?`, person, dedupe).Scan(&n)
	return n > 0
}

// Filter narrows a list.
type Filter struct {
	State string // unread, open (not done; the default), done, all
	Kind  string
	Limit int
}

// List returns a person's notices, newest first.
func (s *Store) List(ctx context.Context, person string, f Filter) ([]Notice, error) {
	q := `SELECT ` + noticeCols + ` FROM notices WHERE recipient = ?`
	args := []any{person}
	switch f.State {
	case "unread":
		q += ` AND read_at = 0 AND done_at = 0`
	case "done":
		q += ` AND done_at <> 0`
	case "all":
	case "", "open":
		q += ` AND done_at = 0`
	default:
		return nil, invalid("state is unread, open, done or all")
	}
	if f.Kind != "" {
		if _, ok := Info(f.Kind); !ok {
			return nil, invalid("%q is not a kind of notice", f.Kind)
		}
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notice{}
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Counts returns how many notices are unread and how many wait (not done).
func (s *Store) Counts(ctx context.Context, person string) (unread, open int, err error) {
	err = s.DB.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND done_at = 0 AND read_at = 0`, person).Scan(&unread)
	if err == nil {
		err = s.DB.QueryRow(ctx, `SELECT count(*) FROM notices WHERE recipient = ? AND done_at = 0`, person).Scan(&open)
	}
	return
}

// MarkRead sets or clears the read mark of some of a person's notices.
func (s *Store) MarkRead(ctx context.Context, person string, idsList []string, read bool) (int, error) {
	return s.mark(ctx, person, idsList, "read_at", read)
}

// MarkDone sets or clears the done mark; done also counts as read.
func (s *Store) MarkDone(ctx context.Context, person string, idsList []string, done bool) (int, error) {
	n, err := s.mark(ctx, person, idsList, "done_at", done)
	if err == nil && done {
		_, err = s.mark(ctx, person, idsList, "read_at", true)
	}
	return n, err
}

func (s *Store) mark(ctx context.Context, person string, idsList []string, col string, on bool) (int, error) {
	if len(idsList) == 0 || len(idsList) > 200 {
		return 0, invalid("name between 1 and 200 notices")
	}
	v := int64(0)
	if on {
		v = s.now().UnixMilli()
	}
	total := 0
	for _, id := range idsList {
		cond := ""
		if on {
			cond = " AND " + col + " = 0"
		}
		res, err := s.DB.Exec(ctx, `UPDATE notices SET `+col+` = ? WHERE id = ? AND recipient = ?`+cond, v, id, person)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// Prefs returns a person's chosen channel per kind (only the chosen).
func (s *Store) Prefs(ctx context.Context, person string) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT kind, channel FROM notice_prefs WHERE person = ?`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, c string
		if err := rows.Scan(&k, &c); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, rows.Err()
}

// SetPref chooses a channel for a kind.
func (s *Store) SetPref(ctx context.Context, person, kind, channel string) error {
	if _, ok := Info(kind); !ok {
		return invalid("%q is not a kind of notice", kind)
	}
	if channel != InApp && channel != Email && channel != Off {
		return invalid("the channel is %s, %s or %s", InApp, Email, Off)
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM notice_prefs WHERE person = ? AND kind = ?`, person, kind); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notice_prefs(person, kind, channel) VALUES(?,?,?)`, person, kind, channel)
	return err
}

// Watch is a person's level on a repository or a Flight.
type Watch struct {
	Scope string `json:"scope"` // repo:owner/name or flight:name
	Level string `json:"level"`
}

// Watches lists a person's chosen levels.
func (s *Store) Watches(ctx context.Context, person string) ([]Watch, error) {
	rows, err := s.DB.Query(ctx, `SELECT scope, level FROM notice_watch WHERE person = ? ORDER BY scope`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Watch{}
	for rows.Next() {
		var w Watch
		if err := rows.Scan(&w.Scope, &w.Level); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ScopeOK reports whether a scope is written right.
func ScopeOK(scope string) bool {
	kind, name, ok := strings.Cut(scope, ":")
	return ok && (kind == "repo" || kind == "flight") && name != "" && len(name) <= 200 && !strings.ContainsAny(name, " \x00\r\n")
}

// SetWatch chooses a level for a scope; participating (the default) removes the row.
func (s *Store) SetWatch(ctx context.Context, person, scope, level string) error {
	if !ScopeOK(scope) {
		return invalid("a scope is repo:owner/name or flight:name")
	}
	if level != WatchAll && level != WatchParticipating && level != WatchNone {
		return invalid("the level is %s, %s or %s", WatchAll, WatchParticipating, WatchNone)
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM notice_watch WHERE person = ? AND scope = ?`, person, scope); err != nil {
		return err
	}
	if level == WatchParticipating {
		return nil
	}
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM notice_watch WHERE person = ?`, person).Scan(&n); err != nil {
		return err
	}
	if n >= 500 {
		return invalid("at most 500 watches per person")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notice_watch(person, scope, level) VALUES(?,?,?)`, person, scope, level)
	return err
}

// Level returns a person's level for the strictest of some scopes: none
// beats all beats participating.
func (s *Store) Level(ctx context.Context, person string, scopes []string) (string, error) {
	level := WatchParticipating
	for _, sc := range scopes {
		var l string
		err := s.DB.QueryRow(ctx, `SELECT level FROM notice_watch WHERE person = ? AND scope = ?`, person, sc).Scan(&l)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		if l == WatchNone {
			return WatchNone, nil
		}
		if l == WatchAll {
			level = WatchAll
		}
	}
	return level, nil
}

// Watchers lists the people who watch any of the scopes at level all.
func (s *Store) Watchers(ctx context.Context, scopes []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, sc := range scopes {
		rows, err := s.DB.Query(ctx, `SELECT person FROM notice_watch WHERE scope = ? AND level = 'all' ORDER BY person LIMIT 1000`, sc)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		rows.Close()
	}
	return out, nil
}

// Settings are a person's quiet hours and digest.
type Settings struct {
	// QuietStart and QuietEnd are minutes after midnight in Zone; -1: no quiet hours.
	QuietStart int    `json:"quiet_start"`
	QuietEnd   int    `json:"quiet_end"`
	Zone       string `json:"zone"`
	Digest     bool   `json:"digest"`
	DigestHour int    `json:"digest_hour"`
}

// Defaults are the settings of a person who chose nothing.
var Defaults = Settings{QuietStart: -1, QuietEnd: -1, Zone: "UTC", DigestHour: 8}

// Settings returns a person's settings.
func (s *Store) Settings(ctx context.Context, person string) (Settings, error) {
	st := Defaults
	var d int
	err := s.DB.QueryRow(ctx, `SELECT quiet_start, quiet_end, zone, digest, digest_hour FROM notice_people WHERE person = ?`, person).Scan(&st.QuietStart, &st.QuietEnd, &st.Zone, &d, &st.DigestHour)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults, nil
	}
	st.Digest = d != 0
	return st, err
}

// Validate checks settings.
func (st Settings) Validate() error {
	if (st.QuietStart < 0) != (st.QuietEnd < 0) || st.QuietStart >= 24*60 || st.QuietEnd >= 24*60 || st.QuietStart < -1 || st.QuietEnd < -1 {
		return invalid("quiet hours are a start and an end, in minutes after midnight (0 to 1439), or both -1 for none")
	}
	if st.QuietStart >= 0 && st.QuietStart == st.QuietEnd {
		return invalid("quiet hours that start when they end cover nothing; use -1 for none")
	}
	if _, err := time.LoadLocation(st.Zone); err != nil || st.Zone == "" {
		return invalid("%q is not a time zone such as Europe/Berlin", st.Zone)
	}
	if st.DigestHour < 0 || st.DigestHour > 23 {
		return invalid("the digest hour is 0 to 23")
	}
	return nil
}

// SetSettings stores a person's settings.
func (s *Store) SetSettings(ctx context.Context, person string, st Settings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	d := 0
	if st.Digest {
		d = 1
	}
	var last int64
	_ = s.DB.QueryRow(ctx, `SELECT last_digest_at FROM notice_people WHERE person = ?`, person).Scan(&last)
	if _, err := s.DB.Exec(ctx, `DELETE FROM notice_people WHERE person = ?`, person); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notice_people(person, quiet_start, quiet_end, zone, digest, digest_hour, last_digest_at) VALUES(?,?,?,?,?,?,?)`,
		person, st.QuietStart, st.QuietEnd, st.Zone, d, st.DigestHour, last)
	return err
}

// QuietUntil returns when quiet hours end if now is inside them, else the zero time.
func (st Settings) QuietUntil(now time.Time) time.Time {
	if st.QuietStart < 0 {
		return time.Time{}
	}
	loc, err := time.LoadLocation(st.Zone)
	if err != nil {
		loc = time.UTC
	}
	l := now.In(loc)
	mins := l.Hour()*60 + l.Minute()
	in := false
	if st.QuietStart < st.QuietEnd {
		in = mins >= st.QuietStart && mins < st.QuietEnd
	} else { // over midnight
		in = mins >= st.QuietStart || mins < st.QuietEnd
	}
	if !in {
		return time.Time{}
	}
	end := time.Date(l.Year(), l.Month(), l.Day(), st.QuietEnd/60, st.QuietEnd%60, 0, 0, loc)
	if !end.After(l) {
		end = end.AddDate(0, 0, 1)
	}
	return end
}
