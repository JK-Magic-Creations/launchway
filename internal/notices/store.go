package notices

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// DB is what the store needs of a database (the SQL is in internal/store).
type DB = store.CtxDB

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

func noticeOf(r store.NoticeRow) Notice {
	return Notice{ID: r.ID, Kind: r.Kind, Repository: r.Repo, Subject: r.Subject, Title: r.Title, Detail: r.Detail, Link: r.Link, Actor: r.Actor, Restricted: r.Restricted,
		At: time.UnixMilli(r.CreatedAt).UTC(), Read: r.ReadAt != 0, Done: r.DoneAt != 0, mailState: r.MailState}
}

// Add stores a notice for a person.
func (s *Store) Add(ctx context.Context, person string, n Notice, mailState, dedupe string) (Notice, error) {
	n.ID = ids.New("ntc")
	now := s.now()
	err := store.AddNotice(ctx, s.DB, person, store.NoticeRow{ID: n.ID, Kind: n.Kind, Repo: n.Repository, Subject: n.Subject, Title: clip(n.Title, 300), Detail: clip(n.Detail, 1000),
		Link: n.Link, Actor: n.Actor, Restricted: n.Restricted, MailState: mailState, CreatedAt: now.UnixMilli()}, dedupe)
	n.At, n.mailState = now.UTC(), mailState
	return n, err
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Seen says whether a person already has a notice with this dedupe key.
func (s *Store) Seen(ctx context.Context, person, dedupe string) bool {
	return dedupe != "" && store.NoticeSeen(ctx, s.DB, person, dedupe)
}

// Filter narrows a list.
type Filter struct {
	State string // unread, open (not done; the default), done, all
	Kind  string
	Limit int
}

// List returns a person's notices, newest first.
func (s *Store) List(ctx context.Context, person string, f Filter) ([]Notice, error) {
	switch f.State {
	case "unread", "done", "all", "", "open":
	default:
		return nil, invalid("state is unread, open, done or all")
	}
	if f.Kind != "" {
		if _, ok := Info(f.Kind); !ok {
			return nil, invalid("%q is not a kind of notice", f.Kind)
		}
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := store.ListNotices(ctx, s.DB, person, f.State, f.Kind, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Notice, 0, len(rows))
	for _, r := range rows {
		out = append(out, noticeOf(r))
	}
	return out, nil
}

// Counts returns how many notices are unread and how many wait (not done).
func (s *Store) Counts(ctx context.Context, person string) (unread, open int, err error) {
	return store.NoticeCounts(ctx, s.DB, person)
}

// MarkRead sets or clears the read mark of some of a person's notices.
func (s *Store) MarkRead(ctx context.Context, person string, idsList []string, read bool) (int, error) {
	return s.mark(ctx, person, idsList, read, store.MarkNoticeRead)
}

// MarkDone sets or clears the done mark; done also counts as read.
func (s *Store) MarkDone(ctx context.Context, person string, idsList []string, done bool) (int, error) {
	n, err := s.mark(ctx, person, idsList, done, store.MarkNoticeDone)
	if err == nil && done {
		_, err = s.mark(ctx, person, idsList, true, store.MarkNoticeRead)
	}
	return n, err
}

func (s *Store) mark(ctx context.Context, person string, idsList []string, on bool, f func(context.Context, store.CtxDB, string, string, int64) (int64, error)) (int, error) {
	if len(idsList) == 0 || len(idsList) > 200 {
		return 0, invalid("name between 1 and 200 notices")
	}
	at := int64(0)
	if on {
		at = s.now().UnixMilli()
	}
	total := 0
	for _, id := range idsList {
		n, err := f(ctx, s.DB, person, id, at)
		if err != nil {
			return total, err
		}
		total += int(n)
	}
	return total, nil
}

// Prefs returns a person's chosen channel per kind (only the chosen).
func (s *Store) Prefs(ctx context.Context, person string) (map[string]string, error) {
	return store.NoticePrefs(ctx, s.DB, person)
}

// SetPref chooses a channel for a kind.
func (s *Store) SetPref(ctx context.Context, person, kind, channel string) error {
	if _, ok := Info(kind); !ok {
		return invalid("%q is not a kind of notice", kind)
	}
	if channel != InApp && channel != Email && channel != Off {
		return invalid("the channel is %s, %s or %s", InApp, Email, Off)
	}
	return store.SetNoticePref(ctx, s.DB, person, kind, channel)
}

// Watch is a person's level on a repository or a Flight.
type Watch struct {
	Scope string `json:"scope"` // repo:owner/name or flight:name
	Level string `json:"level"`
}

// Watches lists a person's chosen levels.
func (s *Store) Watches(ctx context.Context, person string) ([]Watch, error) {
	rows, err := store.NoticeWatches(ctx, s.DB, person)
	if err != nil {
		return nil, err
	}
	out := make([]Watch, 0, len(rows))
	for _, r := range rows {
		out = append(out, Watch{Scope: r.Scope, Level: r.Level})
	}
	return out, nil
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
	if level == WatchParticipating {
		level = ""
	}
	err := store.SetNoticeWatch(ctx, s.DB, person, scope, level, 500)
	if errors.Is(err, store.ErrTooManyWatches) {
		return invalid("at most 500 watches per person")
	}
	return err
}

// Level returns a person's level for the strictest of some scopes: none
// beats all beats participating.
func (s *Store) Level(ctx context.Context, person string, scopes []string) (string, error) {
	level := WatchParticipating
	for _, sc := range scopes {
		l, ok, err := store.NoticeWatchLevel(ctx, s.DB, person, sc)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
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
		people, err := store.NoticeWatchers(ctx, s.DB, sc)
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
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
	r, ok, err := store.NoticePerson(ctx, s.DB, person)
	if err != nil || !ok {
		return Defaults, err
	}
	return Settings{QuietStart: r.QuietStart, QuietEnd: r.QuietEnd, Zone: r.Zone, Digest: r.Digest, DigestHour: r.DigestHour}, nil
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
	return store.SetNoticePerson(ctx, s.DB, person, store.PersonRow{QuietStart: st.QuietStart, QuietEnd: st.QuietEnd, Zone: st.Zone, Digest: st.Digest, DigestHour: st.DigestHour})
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
