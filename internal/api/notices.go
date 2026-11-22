package api

// Notifications people receive: the inbox that unifies the notices, the work
// inbox and the pull requests that wait on a person; each person's choices;
// the mail settings' status and its test message; and the link in mail that
// mutes a kind without signing in. See internal/notices and internal/mail.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/mail"
	"github.com/jk-magic-creations/launchway/internal/notices"
	"github.com/jk-magic-creations/launchway/internal/work"
)

func (s *Server) noticeRoutes() []route {
	return []route{
		{"GET /api/v1/inbox", signedIn, "", s.inboxList},
		{"GET /api/v1/inbox/count", signedIn, "", s.inboxCount},
		{"PUT /api/v1/inbox/read", signedIn, "", s.inboxRead},
		{"PUT /api/v1/inbox/done", signedIn, "", s.inboxDone},
		{"GET /api/v1/notices/preferences", signedIn, "", s.noticePrefsGet},
		{"PUT /api/v1/notices/preferences", signedIn, "", s.noticePrefsSet},
		{"GET /api/v1/mail", administer, "", s.mailStatus},
		{"POST /api/v1/mail/test", administer, "mail.test", s.mailTest},
		{"GET /notices/unsubscribe", public, "", s.unsubscribePage},
		{"POST /notices/unsubscribe", public, "notices.mute", s.unsubscribe},
	}
}

var mailQueueOnce sync.Map

// EnableNotices turns on notifications for people: the notice service always,
// and the mail queue when the installation's mail settings name a server.
func (s *Server) EnableNotices(cfg config.MailConfig) {
	s.MailSettings = cfg
	db := s.Engine.Database()
	svc := &notices.Service{
		Store:          &notices.Store{DB: db, Now: s.Now},
		PublicURL:      cfg.PublicURL,
		PrivateDetails: cfg.IncludePrivateDetails,
		Now:            s.Now,
		Address:        s.personAddress,
		Key: func(ctx context.Context) ([]byte, error) {
			v, err := s.sharedSecret(ctx, "notice_mute_key")
			return []byte(v), err
		},
	}
	if cfg.Enabled() {
		sender := &mail.Sender{Config: cfg.Sender(), Secrets: s.AISecrets, Dial: egress.Default.Dialer(30 * time.Second)}
		s.Mail = &mail.Queue{DB: db, Sender: sender, Now: s.Now, PerMinute: cfg.RatePerMinute, PerHour: cfg.RatePerHour, MaxAttempts: cfg.MaxAttempts,
			Audit: func(_ context.Context, action, person, detail, outcome string) {
				s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "system", Action: action, Target: person, Detail: detail, Outcome: outcome})
			}}
		svc.Mail = s.Mail
	}
	s.Notices = svc
	s.Engine.SetPersonNotifier(s.personEvent)
	if s.Forge != nil && s.Forge.Pulls != nil {
		_ = s.Forge // pull events reach noticePull through pullEvent
	}
	s.work().SetInboxed(s.workInboxed)
}

// personAddress is the address a person's mail goes to: the one in their
// profile as of their last sign-in.
func (s *Server) personAddress(_ context.Context, person string) string {
	_, prof, err := s.Engine.LastSignIn(person)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(prof.Email)
}

// RunNotices sends mail, digests and warnings of expiring credentials in the
// background until ctx ends.
func (s *Server) RunNotices(ctx context.Context) {
	if s.Notices == nil {
		return
	}
	if s.Mail != nil {
		go s.Mail.Run(ctx, 15*time.Second)
	}
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for n := 0; ; n++ {
		_, _ = s.Notices.RunDigests(ctx)
		if n%12 == 0 {
			s.scanCredentials(ctx)
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// ExpiryWarning is how long before a credential's end its owner is told.
const ExpiryWarning = 14 * 24 * time.Hour

// scanCredentials tells people whose SSH keys, signing keys or API tokens end
// within two weeks, once for each.
func (s *Server) scanCredentials(ctx context.Context) {
	db := s.Engine.Database()
	now := s.Now()
	type cred struct {
		owner, what, name string
		at                int64
	}
	var found []cred
	for _, q := range []struct{ what, sql string }{
		{"SSH key", `SELECT owner, name, expires_at FROM ssh_keys WHERE kind = 'user' AND expires_at > ? AND expires_at < ?`},
		{"signing key", `SELECT owner, name, expires_at FROM signing_keys WHERE expires_at > ? AND expires_at < ? AND revoked_at = 0`},
		{"API token", `SELECT username, name, expires_at FROM api_tokens WHERE expires_at > ? AND expires_at < ?`},
	} {
		rows, err := db.Query(ctx, q.sql, now.UnixMilli(), now.Add(ExpiryWarning).UnixMilli())
		if err != nil {
			continue
		}
		for rows.Next() {
			c := cred{what: q.what}
			if rows.Scan(&c.owner, &c.name, &c.at) == nil {
				found = append(found, c)
			}
		}
		rows.Close()
	}
	for _, c := range found {
		day := time.UnixMilli(c.at).UTC().Format("2006-01-02")
		_, _ = s.Notices.Notify(ctx, notices.Event{Kind: notices.CredentialExpiring, People: []string{c.owner}, Name: "your account",
			Title: fmt.Sprintf("your %s %q ends on %s", c.what, c.name, day), Link: "/settings/access/keys", Dedupe: "cred:" + c.what + ":" + c.name + ":" + day})
	}
}

// ---- the inbox ----

// inboxItem is one line of the unified inbox, whichever source it comes from.
type inboxItem struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"` // notice, work or pull
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail,omitempty"`
	Repository string    `json:"repository,omitempty"`
	Link       string    `json:"link,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	At         time.Time `json:"at"`
	Read       bool      `json:"read"`
	Done       bool      `json:"done"`
	// Waiting items resolve themselves when what they wait for is done; they
	// cannot be marked.
	Waiting bool `json:"waiting_on_you,omitempty"`
}

func (s *Server) noticesOn(w http.ResponseWriter) bool {
	if s.Notices == nil {
		writeFeatureOff(w)
		return false
	}
	return true
}

// inboxFor gathers a person's inbox from its three sources.
func (s *Server) inboxFor(ctx context.Context, id *identity, state, kind, source string) ([]inboxItem, error) {
	var out []inboxItem
	if source == "" || source == "notice" {
		ns, err := s.Notices.Store.List(ctx, id.User, notices.Filter{State: state, Kind: kind, Limit: 200})
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			out = append(out, inboxItem{ID: n.ID, Source: "notice", Kind: n.Kind, Title: n.Title, Detail: n.Detail, Repository: n.Repository, Link: n.Link, Actor: n.Actor, At: n.At, Read: n.Read, Done: n.Done,
				Waiting: false})
		}
	}
	if kind == "" && (source == "" || source == "work") && state != "done" {
		es, _, err := s.work().Inbox(ctx, id.User, state == "unread", 100)
		if err == nil {
			for _, e := range es {
				if e.Kind == work.InboxMentioned || e.Kind == work.InboxComment {
					continue // these are notices now
				}
				at, _ := time.Parse(time.RFC3339, e.At)
				out = append(out, inboxItem{ID: fmt.Sprintf("work:%d", e.ID), Source: "work", Kind: "work_" + e.Kind, Title: e.Text, Link: "/work", At: at, Read: e.Read})
			}
		}
	}
	if kind == "" && (source == "" || source == "pull") && state != "done" && state != "unread" && s.Forge != nil && s.Forge.Pulls != nil {
		items, err := s.Forge.Pulls.Store.Inbox(ctx, id.User, auth.StripReservedGroups(id.Groups))
		if err == nil {
			for _, it := range items {
				repo, err := s.Forge.RepoByID(ctx, it.RepoID)
				if err != nil || !s.authorize(id, auth.Read, repo.Path) {
					continue
				}
				out = append(out, inboxItem{ID: fmt.Sprintf("pull:%s#%d", repo.Path, it.Number), Source: "pull", Kind: "pull_" + it.Kind, Title: fmt.Sprintf("#%d %s", it.Number, it.Title), Detail: it.Why,
					Repository: repo.Path, Link: fmt.Sprintf("/repositories/%s/pulls/%d", repo.Path, it.Number), At: it.At, Waiting: true})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b inboxItem) int { return b.At.Compare(a.At) })
	if out == nil {
		out = []inboxItem{}
	}
	return out, nil
}

func (s *Server) inboxList(w http.ResponseWriter, r *http.Request) {
	if !s.noticesOn(w) {
		return
	}
	q := r.URL.Query()
	items, err := s.inboxFor(r.Context(), identityFrom(r), q.Get("state"), q.Get("kind"), q.Get("source"))
	if err != nil {
		if errors.Is(err, notices.ErrInvalid) {
			writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid choice: "))
			return
		}
		s.fail(w, err)
		return
	}
	unread, waiting := s.inboxCounts(r.Context(), identityFrom(r))
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unread": unread, "waiting": waiting})
}

func (s *Server) inboxCounts(ctx context.Context, id *identity) (unread, waiting int) {
	n, _, _ := s.Notices.Store.Counts(ctx, id.User)
	unread = n
	if _, wu, err := s.work().Inbox(ctx, id.User, true, 1); err == nil {
		unread += wu
	}
	if items, err := s.inboxFor(ctx, id, "open", "", "pull"); err == nil {
		waiting = len(items)
	}
	return
}

func (s *Server) inboxCount(w http.ResponseWriter, r *http.Request) {
	if !s.noticesOn(w) {
		return
	}
	unread, waiting := s.inboxCounts(r.Context(), identityFrom(r))
	writeJSON(w, http.StatusOK, map[string]int{"unread": unread, "waiting": waiting})
}

type inboxMark struct {
	IDs   []string `json:"ids"`
	Value *bool    `json:"value"` // default true; false clears the mark
}

func (s *Server) inboxMarks(w http.ResponseWriter, r *http.Request, done bool) {
	if !s.noticesOn(w) {
		return
	}
	var in inboxMark
	if !decode(w, r, &in) {
		return
	}
	on := in.Value == nil || *in.Value
	id := identityFrom(r)
	var mine []string
	var theWork []int64
	for _, x := range in.IDs {
		switch {
		case strings.HasPrefix(x, "work:"):
			var n int64
			if _, err := fmt.Sscanf(x, "work:%d", &n); err == nil {
				theWork = append(theWork, n)
			}
		case strings.HasPrefix(x, "pull:"):
			writeErr(w, http.StatusBadRequest, "a pull request that waits on you leaves the inbox when it is dealt with; it cannot be marked")
			return
		default:
			mine = append(mine, x)
		}
	}
	var n int
	var err error
	if len(mine) > 0 {
		if done {
			n, err = s.Notices.Store.MarkDone(r.Context(), id.User, mine, on)
		} else {
			n, err = s.Notices.Store.MarkRead(r.Context(), id.User, mine, on)
		}
	}
	if err == nil && len(theWork) > 0 && on {
		err = s.work().MarkRead(r.Context(), id.User, theWork)
		n += len(theWork)
	}
	if err != nil {
		if errors.Is(err, notices.ErrInvalid) {
			writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid choice: "))
			return
		}
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"changed": n})
}

func (s *Server) inboxRead(w http.ResponseWriter, r *http.Request) { s.inboxMarks(w, r, false) }
func (s *Server) inboxDone(w http.ResponseWriter, r *http.Request) { s.inboxMarks(w, r, true) }

// ---- a person's choices ----

type kindChoice struct {
	notices.KindInfo
	Channel string `json:"channel"`
}

type prefsOut struct {
	Kinds    []kindChoice     `json:"kinds"`
	Watches  []notices.Watch  `json:"watches"`
	Settings notices.Settings `json:"settings"`
	MailOn   bool             `json:"mail_on"`
	Address  bool             `json:"address_known"`
	Channels []string         `json:"channels"`
	Levels   []string         `json:"levels"`
}

func (s *Server) prefsFor(ctx context.Context, user string) (prefsOut, error) {
	prefs, err := s.Notices.Store.Prefs(ctx, user)
	if err != nil {
		return prefsOut{}, err
	}
	out := prefsOut{Kinds: []kindChoice{}, MailOn: s.Mail != nil, Address: s.personAddress(ctx, user) != "",
		Channels: []string{notices.InApp, notices.Email, notices.Off}, Levels: []string{notices.WatchAll, notices.WatchParticipating, notices.WatchNone}}
	for _, k := range notices.Kinds {
		ch := k.Default
		if c, ok := prefs[k.Kind]; ok {
			ch = c
		}
		out.Kinds = append(out.Kinds, kindChoice{k, ch})
	}
	if out.Watches, err = s.Notices.Store.Watches(ctx, user); err != nil {
		return out, err
	}
	out.Settings, err = s.Notices.Store.Settings(ctx, user)
	return out, err
}

func (s *Server) noticePrefsGet(w http.ResponseWriter, r *http.Request) {
	if !s.noticesOn(w) {
		return
	}
	out, err := s.prefsFor(r.Context(), identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type prefsIn struct {
	// Kinds maps a kind to in_app, email or off.
	Kinds map[string]string `json:"kinds"`
	// Watches sets levels; participating removes a watch.
	Watches  []notices.Watch   `json:"watches"`
	Settings *notices.Settings `json:"settings"`
}

func (s *Server) noticePrefsSet(w http.ResponseWriter, r *http.Request) {
	if !s.noticesOn(w) {
		return
	}
	var in prefsIn
	if !decode(w, r, &in) {
		return
	}
	user := identityFrom(r).User
	ctx := r.Context()
	st := s.Notices.Store
	var err error
	for _, k := range slices.Sorted(mapsKeys(in.Kinds)) {
		if err = st.SetPref(ctx, user, k, in.Kinds[k]); err != nil {
			break
		}
	}
	for i := 0; err == nil && i < len(in.Watches); i++ {
		err = st.SetWatch(ctx, user, in.Watches[i].Scope, in.Watches[i].Level)
	}
	if err == nil && in.Settings != nil {
		err = st.SetSettings(ctx, user, *in.Settings)
	}
	if err != nil {
		if errors.Is(err, notices.ErrInvalid) {
			writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid choice: "))
			return
		}
		s.fail(w, err)
		return
	}
	out, err := s.prefsFor(ctx, user)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// ---- the mail settings ----

type mailOut struct {
	Enabled   bool   `json:"enabled"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	Security  string `json:"security,omitempty"`
	From      string `json:"from,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	// Credentials says whether a user name and password are set; they are
	// references, never shown.
	Credentials           bool `json:"credentials"`
	IncludePrivateDetails bool `json:"include_private_details"`
	Pending               int  `json:"pending"`
	Sent                  int  `json:"sent"`
	Failed                int  `json:"failed"`
	SentLastHour          int  `json:"sent_last_hour"`
	RatePerMinute         int  `json:"rate_per_minute"`
	RatePerHour           int  `json:"rate_per_hour"`
}

func (s *Server) mailStatus(w http.ResponseWriter, r *http.Request) {
	c := s.MailSettings
	out := mailOut{Enabled: s.Mail != nil, Host: c.Host, Port: c.Sender().Port, Security: c.Security, From: c.From, PublicURL: c.PublicURL, Credentials: c.Username != nil,
		IncludePrivateDetails: c.IncludePrivateDetails, RatePerMinute: c.RatePerMinute, RatePerHour: c.RatePerHour}
	if out.Enabled && out.Security == "" {
		out.Security = mail.StartTLS
	}
	if out.RatePerMinute == 0 {
		out.RatePerMinute = mail.DefaultPerMinute
	}
	if out.RatePerHour == 0 {
		out.RatePerHour = mail.DefaultPerHour
	}
	if s.Mail != nil {
		if n, err := s.Mail.Counts(r.Context()); err == nil {
			out.Pending, out.Sent, out.Failed, out.SentLastHour = n.Pending, n.Sent, n.Failed, n.SentLastHour
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mailTest(w http.ResponseWriter, r *http.Request) {
	if s.Mail == nil {
		writeErr(w, http.StatusConflict, mail.ErrNotConfigured.Error())
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	to := strings.TrimSpace(in.To)
	if to == "" {
		to = s.personAddress(r.Context(), id.User)
	}
	if to == "" {
		writeErr(w, http.StatusBadRequest, "say where to send it (to), or add an address to your profile")
		return
	}
	setAudit(r, id.User, "test message")
	if err := s.Mail.SendTest(r.Context(), to); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"sent": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "to": to})
}

// ---- the mute link ----

const unsubscribeHead = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="referrer" content="no-referrer"><title>Mail from Launchway</title>` +
	`<style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem}button{font:inherit;padding:.5rem 1rem}</style></head><body>`

func (s *Server) muteToken(r *http.Request) (person, kind string, err error) {
	key, kerr := s.Notices.Key(r.Context())
	if kerr != nil {
		return "", "", kerr
	}
	tok := r.URL.Query().Get("token")
	if tok == "" {
		tok = r.FormValue("token")
	}
	return notices.VerifyMute(key, tok, s.Now())
}

func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	_, kind, err := s.muteToken(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, unsubscribeHead+"<h1>This link does not work</h1><p>"+html.EscapeString(err.Error())+" Sign in to Launchway and change what you are sent under Settings, Notifications.</p></body></html>")
		return
	}
	info, _ := notices.Info(kind)
	fmt.Fprintf(w, unsubscribeHead+`<h1>Stop this kind of mail?</h1><p>You will no longer be e-mailed when: %s. It still appears in your inbox in Launchway.</p>`+
		`<form method="post" action="/notices/unsubscribe?token=%s"><button type="submit">Stop these e-mails</button></form></body></html>`, html.EscapeString(strings.ToLower(info.Title)), url.QueryEscape(r.URL.Query().Get("token")))
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Notices == nil {
		http.NotFound(w, r)
		return
	}
	person, kind, err := s.muteToken(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Notices.Mute(r.Context(), person, kind); err != nil {
		http.Error(w, "that could not be saved; try again", http.StatusInternalServerError)
		return
	}
	setAudit(r, person, "muted mail of "+kind)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	info, _ := notices.Info(kind)
	fmt.Fprint(w, unsubscribeHead+"<h1>Done</h1><p>You will no longer be e-mailed when: "+html.EscapeString(strings.ToLower(info.Title))+". It still appears in your inbox in Launchway.</p></body></html>")
}
