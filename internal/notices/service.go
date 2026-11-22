package notices

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/mail"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Event is something that happened that concerns people.
type Event struct {
	Kind string
	// People are those it is for: who was asked, who took part, whose run it was.
	People []string
	// Scopes are the repository and Flight it belongs to (repo:owner/name,
	// flight:name): a person's watch level on them decides whether they are told,
	// and those who watch everything there are told too (activity kinds).
	Scopes []string
	// Actor did it; they are never told of their own doing.
	Actor string
	// Name is what the message calls the place: the repository or the Flight.
	Name string
	// Public says that what happened may be written into mail: the repository
	// is public. Otherwise mail carries only the place and a link.
	Public bool
	// Title and Detail are the content: a pull request's title, a comment.
	Title  string
	Detail string
	Link   string // an address in the product, starting with /
	// Dedupe stops the same notice reaching a person twice.
	Dedupe string
}

// Service makes notices and sends the mail they ask for.
type Service struct {
	Store *Store
	// Mail is the queue mail goes into (nil: no mail is sent).
	Mail *mail.Queue
	// Address finds a person's e-mail address ("" if none is known).
	Address func(ctx context.Context, person string) string
	// PublicURL is where links in mail lead: the installation's address.
	PublicURL string
	// PrivateDetails lets titles and comments of repositories that are not
	// public travel by mail (mail.include_private_details).
	PrivateDetails bool
	// Key is the key that signs mute links.
	Key func(ctx context.Context) ([]byte, error)
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Channel returns the channel a person has for a kind.
func (s *Service) Channel(ctx context.Context, person, kind string) (string, error) {
	prefs, err := s.Store.Prefs(ctx, person)
	if err != nil {
		return "", err
	}
	return channelOf(prefs, kind), nil
}

func channelOf(prefs map[string]string, kind string) string {
	if c, ok := prefs[kind]; ok {
		return c
	}
	if k, ok := Info(kind); ok {
		return k.Default
	}
	return InApp
}

// Notify makes the notices an event asks for, and queues the mail that goes
// with them. It returns the people who got a notice.
func (s *Service) Notify(ctx context.Context, ev Event) ([]string, error) {
	info, ok := Info(ev.Kind)
	if !ok {
		return nil, fmt.Errorf("%q is not a kind of notice", ev.Kind)
	}
	people := slices.Clone(ev.People)
	if info.Class == Activity {
		w, err := s.Store.Watchers(ctx, ev.Scopes)
		if err != nil {
			return nil, err
		}
		people = append(people, w...)
	}
	slices.Sort(people)
	people = slices.Compact(people)
	var told []string
	for _, p := range people {
		if p == "" || p == ev.Actor {
			continue
		}
		level, err := s.Store.Level(ctx, p, ev.Scopes)
		if err != nil {
			return told, err
		}
		if level == WatchNone {
			continue
		}
		prefs, err := s.Store.Prefs(ctx, p)
		if err != nil {
			return told, err
		}
		ch := channelOf(prefs, ev.Kind)
		if ch == Off || s.Store.Seen(ctx, p, ev.Dedupe) {
			continue
		}
		n := Notice{Kind: ev.Kind, Repository: ev.Name, Subject: ev.Title, Title: titleFor(ev), Detail: ev.Detail, Link: ev.Link, Actor: ev.Actor, Restricted: !ev.Public}
		state := ""
		if ch == Email && s.Mail != nil {
			state = "queued"
			if st, err := s.Store.Settings(ctx, p); err == nil && st.Digest {
				state = "digest"
			}
		}
		saved, err := s.Store.Add(ctx, p, n, state, ev.Dedupe)
		if err != nil {
			return told, err
		}
		told = append(told, p)
		if state == "queued" {
			if err := s.queue(ctx, p, saved); err != nil {
				store.SetNoticeMailState(ctx, s.Store.DB, saved.ID, "skipped")
			}
		}
	}
	return told, nil
}

func titleFor(ev Event) string {
	if ev.Title != "" {
		return fmt.Sprintf("%s: %s", phraseCap(ev.Kind), ev.Title)
	}
	return phraseCap(ev.Kind)
}

func phraseCap(kind string) string {
	p := phrase(kind)
	return strings.ToUpper(p[:1]) + p[1:]
}

func (s *Service) allowDetail(n Notice) bool { return !n.Restricted || s.PrivateDetails }

func (s *Service) absolute(path string) string {
	if s.PublicURL == "" || !strings.HasPrefix(path, "/") {
		return ""
	}
	return strings.TrimRight(s.PublicURL, "/") + path
}

// subject is what a message's subject says: the place and what happened, and
// the title only where mail may carry it.
func (s *Service) subject(n Notice) string {
	var b strings.Builder
	if n.Repository != "" {
		b.WriteString("[" + n.Repository + "] ")
	} else {
		b.WriteString("[Launchway] ")
	}
	b.WriteString(phrase(n.Kind))
	if n.Subject != "" && s.allowDetail(n) {
		b.WriteString(": " + n.Subject)
	}
	return clip(b.String(), 200)
}

// queue makes a notice's mail.
func (s *Service) queue(ctx context.Context, person string, n Notice) error {
	to := ""
	if s.Address != nil {
		to = s.Address(ctx, person)
	}
	if to == "" {
		return fmt.Errorf("no address for %s", person)
	}
	text, htm, err := s.bodies(ctx, person, []Notice{n}, false)
	if err != nil {
		return err
	}
	it := mail.Item{Person: person, To: to, Kind: n.Kind, Subject: s.subject(n), Text: text, HTML: htm}
	if st, err := s.Store.Settings(ctx, person); err == nil {
		it.NotBefore = st.QuietUntil(s.now())
	}
	_, err = s.Mail.Enqueue(ctx, it)
	return err
}

// bodies writes the plain-text and the HTML parts of a message about one or
// several notices. HTML has paragraphs and links and nothing remote.
func (s *Service) bodies(ctx context.Context, person string, ns []Notice, digest bool) (string, string, error) {
	var t, h strings.Builder
	if digest {
		fmt.Fprintf(&t, "%d things since your last digest:\n\n", len(ns))
		fmt.Fprintf(&h, "<p>%d things since your last digest:</p>\n<ul>\n", len(ns))
	}
	for _, n := range ns {
		line := phraseCap(n.Kind)
		if n.Repository != "" {
			line += " in " + n.Repository
		}
		if n.Actor != "" {
			line += " (by " + n.Actor + ")"
		}
		detail := ""
		if s.allowDetail(n) {
			switch {
			case n.Subject != "" && n.Detail != "":
				detail = n.Subject + "\n" + n.Detail
			case n.Subject != "":
				detail = n.Subject
			default:
				detail = n.Detail
			}
		}
		link := s.absolute(n.Link)
		if digest {
			t.WriteString("- " + line + "\n")
			h.WriteString("<li>" + html.EscapeString(line))
		} else {
			t.WriteString(line + "\n")
			h.WriteString("<p>" + html.EscapeString(line) + "</p>\n")
		}
		if detail != "" {
			t.WriteString(indent(detail) + "\n")
			h.WriteString("<blockquote>" + strings.ReplaceAll(html.EscapeString(detail), "\n", "<br>") + "</blockquote>")
		} else if n.Restricted {
			t.WriteString("  (The details are left out of mail for repositories that are not public: open the link.)\n")
		}
		if link != "" {
			t.WriteString("  " + link + "\n")
			fmt.Fprintf(&h, ` <a href="%s">Open it</a>`, html.EscapeString(link))
		}
		if digest {
			h.WriteString("</li>\n")
		} else {
			t.WriteString("\n")
		}
	}
	if digest {
		h.WriteString("</ul>\n")
	}
	prefs := s.absolute("/settings/notifications")
	var mute string
	if key, err := s.Key(ctx); err == nil && len(ns) == 1 {
		mute = s.absolute("/notices/unsubscribe?token=" + url.QueryEscape(SignMute(key, person, ns[0].Kind, s.now())))
	}
	t.WriteString("--\nYou get this because of your notification choices.")
	h.WriteString("<hr><p>You get this because of your notification choices.")
	if prefs != "" {
		t.WriteString("\nChange them: " + prefs)
		fmt.Fprintf(&h, ` <a href="%s">Change them</a>.`, html.EscapeString(prefs))
	}
	if mute != "" {
		t.WriteString("\nMute this kind of message: " + mute)
		fmt.Fprintf(&h, ` <a href="%s">Mute this kind of message</a>.`, html.EscapeString(mute))
	}
	t.WriteString("\n")
	h.WriteString("</p>\n")
	return t.String(), h.String(), nil
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

// Mute turns off the mail of one kind for a person (they still see it in the
// inbox): what the signed link does.
func (s *Service) Mute(ctx context.Context, person, kind string) error {
	prefs, err := s.Store.Prefs(ctx, person)
	if err != nil {
		return err
	}
	if channelOf(prefs, kind) == Email {
		return s.Store.SetPref(ctx, person, kind, InApp)
	}
	return nil
}

// RunDigests sends the digests that are due: for each person who chose a
// digest, once a day at their hour in their zone, one message with what
// waited. It is called every few minutes.
func (s *Service) RunDigests(ctx context.Context) (int, error) {
	if s.Mail == nil {
		return 0, nil
	}
	people, err := store.DigestPeople(ctx, s.Store.DB)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, p := range people {
		st, err := s.Store.Settings(ctx, p)
		if err != nil {
			continue
		}
		loc, lerr := time.LoadLocation(st.Zone)
		if lerr != nil {
			loc = time.UTC
		}
		local := s.now().In(loc)
		dayStart := time.Date(local.Year(), local.Month(), local.Day(), st.DigestHour, 0, 0, 0, loc)
		row, _, _ := store.NoticePerson(ctx, s.Store.DB, p)
		if local.Before(dayStart) || row.LastDigestAt >= dayStart.UnixMilli() {
			continue
		}
		ns, err := s.waiting(ctx, p)
		if err != nil {
			continue
		}
		store.SetLastDigest(ctx, s.Store.DB, p, s.now().UnixMilli())
		if len(ns) == 0 {
			continue
		}
		to := ""
		if s.Address != nil {
			to = s.Address(ctx, p)
		}
		if to == "" {
			continue
		}
		text, htm, err := s.bodies(ctx, p, ns, true)
		if err != nil {
			continue
		}
		it := mail.Item{Person: p, To: to, Kind: "digest", Subject: fmt.Sprintf("[Launchway] your digest: %d things", len(ns)), Text: text, HTML: htm}
		if _, err := s.Mail.Enqueue(ctx, it); err != nil {
			continue
		}
		for _, n := range ns {
			store.SetNoticeMailState(ctx, s.Store.DB, n.ID, "queued")
		}
		sent++
	}
	return sent, nil
}

func (s *Service) waiting(ctx context.Context, person string) ([]Notice, error) {
	rows, err := store.DigestNotices(ctx, s.Store.DB, person)
	if err != nil {
		return nil, err
	}
	out := make([]Notice, 0, len(rows))
	for _, r := range rows {
		out = append(out, noticeOf(r))
	}
	return out, nil
}

// ScanCredentials tells the people whose SSH keys, signing keys or API tokens
// end within a time, once for each credential.
func (s *Service) ScanCredentials(ctx context.Context, within time.Duration) {
	now := s.now()
	for _, c := range store.ExpiringCredentials(ctx, s.Store.DB, now.UnixMilli(), now.Add(within).UnixMilli()) {
		day := time.UnixMilli(c.At).UTC().Format("2006-01-02")
		_, _ = s.Notify(ctx, Event{Kind: CredentialExpiring, People: []string{c.Owner}, Name: "your account",
			Title: fmt.Sprintf("your %s %q ends on %s", c.What, c.Name, day), Link: "/settings/access/keys", Dedupe: "cred:" + c.What + ":" + c.Name + ":" + day})
	}
}
