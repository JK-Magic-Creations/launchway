package notices

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/mail"
	"github.com/jk-magic-creations/launchway/internal/mail/smtptest"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type world struct {
	t   *testing.T
	svc *Service
	srv *smtptest.Server
	q   *mail.Queue
	now time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	srv, err := smtptest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	w := &world{t: t, srv: srv, now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	db := eng.Database()
	w.q = &mail.Queue{DB: db, Now: func() time.Time { return w.now },
		Sender: &mail.Sender{Config: mail.Config{Host: srv.Host, Port: srv.Port, From: "Launchway <notify@example.com>", TLSConfig: srv.ClientTLS()}}}
	w.svc = &Service{Store: &Store{DB: db, Now: func() time.Time { return w.now }}, Mail: w.q, PublicURL: "https://launchway.example.com", Now: func() time.Time { return w.now },
		Address: func(_ context.Context, p string) string { return p + "@example.com" },
		Key:     func(context.Context) ([]byte, error) { return []byte("test-key-0123456789"), nil }}
	return w
}

func (w *world) notify(ev Event) []string {
	w.t.Helper()
	told, err := w.svc.Notify(t0(), ev)
	if err != nil {
		w.t.Fatal(err)
	}
	return told
}

func t0() context.Context { return context.Background() }

func (w *world) deliver() []smtptest.Received {
	w.t.Helper()
	if _, err := w.q.RunOnce(t0()); err != nil {
		w.t.Fatal(err)
	}
	return w.srv.Messages()
}

var review = Event{Kind: ReviewRequested, People: []string{"bob"}, Scopes: []string{"repo:acme/shop"}, Actor: "ada", Name: "acme/shop",
	Title: "Rework the checkout", Detail: "please look at the totals", Link: "/code/acme/shop/pulls/12"}

func TestDefaultsEmailWhatWaitsOnYouAndKeepTheRestInTheInbox(t *testing.T) {
	w := newWorld(t)
	w.notify(review) // waiting on bob: inbox and mail
	c := review
	c.Kind, c.Public = Comment, true
	w.notify(c) // activity: inbox only
	got := w.deliver()
	if len(got) != 1 || got[0].To != "bob@example.com" || !strings.Contains(got[0].Data, "review requested") {
		t.Fatalf("mail: %+v", got)
	}
	ns, err := w.svc.Store.List(t0(), "bob", Filter{})
	if err != nil || len(ns) != 2 {
		t.Fatalf("inbox: %+v %v", ns, err)
	}
	if told := w.notify(Event{Kind: ReviewRequested, People: []string{"ada"}, Actor: "ada", Scopes: review.Scopes}); len(told) != 0 {
		t.Fatalf("someone was told of their own doing: %v", told)
	}
}

func TestMailOfAPrivateRepositoryCarriesOnlyItsNameAndALink(t *testing.T) {
	w := newWorld(t)
	w.notify(review)
	pub := review
	pub.Public, pub.Name, pub.Title, pub.Detail = true, "acme/site", "Public title", "a public comment"
	w.notify(pub)
	got := w.deliver()
	if len(got) != 2 {
		t.Fatalf("%d messages", len(got))
	}
	private, public := got[0].Data, got[1].Data
	for _, secret := range []string{"Rework the checkout", "please look at the totals", "totals"} {
		if strings.Contains(private, secret) {
			t.Errorf("a private repository's content is in mail: %q\n%s", secret, private)
		}
	}
	if !strings.Contains(private, "Subject: [acme/shop] review requested\r\n") || !strings.Contains(private, "https://launchway.example.com/code/acme/shop/pulls/12") {
		t.Errorf("the subject or the link is wrong:\n%s", private)
	}
	if !strings.Contains(public, "Subject: [acme/site] review requested: Public title") || !strings.Contains(public, "a public comment") {
		t.Errorf("a public repository's mail should carry its content:\n%s", public)
	}
	// The administrator may allow the content of private repositories.
	w.svc.PrivateDetails = true
	r2 := review
	r2.People = []string{"carol"}
	w.notify(r2)
	got = w.deliver()
	if last := got[len(got)-1].Data; !strings.Contains(last, "Rework the checkout") {
		t.Errorf("mail.include_private_details was ignored:\n%s", last)
	}
	for _, m := range got {
		if strings.Contains(m.Data, "<img") || strings.Contains(strings.ToLower(m.Data), "src=") {
			t.Errorf("remote content in mail:\n%s", m.Data)
		}
	}
}

func TestPreferencesAndWatchLevelsDecideWhoIsToldAndHow(t *testing.T) {
	w := newWorld(t)
	st := w.svc.Store
	_ = st.SetPref(t0(), "bob", ReviewRequested, InApp)
	_ = st.SetPref(t0(), "carol", ReviewRequested, Off)
	_ = st.SetPref(t0(), "dave", Comment, Email)
	_ = st.SetWatch(t0(), "erin", "repo:acme/shop", WatchAll)
	_ = st.SetWatch(t0(), "frank", "repo:acme/shop", WatchNone)
	told := w.notify(Event{Kind: ReviewRequested, People: []string{"bob", "carol", "frank"}, Scopes: []string{"repo:acme/shop"}, Actor: "ada", Name: "acme/shop"})
	if strings.Join(told, ",") != "bob" {
		t.Fatalf("told %v: carol turned the kind off and frank mutes the repository", told)
	}
	told = w.notify(Event{Kind: Comment, People: []string{"dave", "frank"}, Scopes: []string{"repo:acme/shop"}, Actor: "ada", Name: "acme/shop", Public: true})
	if strings.Join(told, ",") != "dave,erin" {
		t.Fatalf("told %v: dave takes part, erin watches everything, frank watches nothing", told)
	}
	got := w.deliver()
	if len(got) != 1 || got[0].To != "dave@example.com" {
		t.Fatalf("only dave chose mail for comments: %+v", got)
	}
	if err := st.SetPref(t0(), "bob", "nonsense", Email); err == nil {
		t.Fatal("a made-up kind was accepted")
	}
	if err := st.SetWatch(t0(), "bob", "elsewhere", WatchAll); err == nil {
		t.Fatal("a made-up scope was accepted")
	}
}

func TestQuietHoursHoldMailAndTheDigestSendsOnceADay(t *testing.T) {
	w := newWorld(t)
	st := w.svc.Store
	if err := st.SetSettings(t0(), "bob", Settings{QuietStart: 11 * 60, QuietEnd: 14 * 60, Zone: "UTC", DigestHour: 8}); err != nil { // now is 12:00
		t.Fatal(err)
	}
	w.notify(review)
	if got := w.deliver(); len(got) != 0 {
		t.Fatalf("mail went out during quiet hours: %d", len(got))
	}
	w.now = w.now.Add(2*time.Hour + time.Minute) // 14:01
	if got := w.deliver(); len(got) != 1 {
		t.Fatalf("mail did not follow after quiet hours: %d", len(got))
	}

	// A digest: nothing goes out as it happens; one message follows at the hour.
	if err := st.SetSettings(t0(), "carol", Settings{QuietStart: -1, QuietEnd: -1, Zone: "UTC", Digest: true, DigestHour: 16}); err != nil {
		t.Fatal(err)
	}
	r := review
	r.People = []string{"carol"}
	w.notify(r)
	r.Name = "acme/other"
	w.notify(r)
	if got := w.deliver(); len(got) != 1 {
		t.Fatalf("carol's mail went out although she chose a digest: %d", len(got))
	}
	if n, _ := w.svc.RunDigests(t0()); n != 0 {
		t.Fatal("the digest went out before its hour")
	}
	w.now = w.now.Add(2 * time.Hour) // 16:01
	if n, err := w.svc.RunDigests(t0()); err != nil || n != 1 {
		t.Fatalf("digest: %d %v", n, err)
	}
	if n, _ := w.svc.RunDigests(t0()); n != 0 {
		t.Fatal("a second digest on the same day")
	}
	got := w.deliver()
	if len(got) != 2 || !strings.Contains(got[1].Data, "2 things") || !strings.Contains(got[1].Data, "acme/other") {
		t.Fatalf("%+v", got)
	}
}

func TestMuteLinksAreSignedAndOnlyMuteMail(t *testing.T) {
	w := newWorld(t)
	w.notify(review)
	got := w.deliver()
	if len(got) != 1 {
		t.Fatal("no mail")
	}
	d := got[0].Data
	i := strings.Index(d, "https://launchway.example.com/notices/unsubscribe?token=")
	if i < 0 || !strings.Contains(d, "List-Unsubscribe: <https://launchway.example.com/notices/unsubscribe?token=") || !strings.Contains(d, "List-Unsubscribe-Post: List-Unsubscribe=One-Click") {
		t.Fatalf("no mute link or header:\n%s", d)
	}
	link := d[i:]
	link = link[:strings.IndexAny(link, " \r\n")]
	// quoted-printable soft breaks may split the link in the raw message: take the token from a fresh signature instead.
	key := []byte("test-key-0123456789")
	tok := SignMute(key, "bob", ReviewRequested, w.now)
	person, kind, err := VerifyMute(key, tok, w.now)
	if err != nil || person != "bob" || kind != ReviewRequested {
		t.Fatalf("%q %q %v", person, kind, err)
	}
	if _, _, err := VerifyMute(key, strings.Replace(tok, "review_requested", "comment", 1), w.now); err == nil {
		t.Fatal("a token for another kind was accepted")
	}
	if _, _, err := VerifyMute([]byte("other-key-0123456789"), tok, w.now); err == nil {
		t.Fatal("a token signed by another key was accepted")
	}
	if _, _, err := VerifyMute(key, tok, w.now.Add(TokenLife+time.Hour)); err == nil {
		t.Fatal("an expired token was accepted")
	}
	for _, bad := range []string{"", "a.b.c.d", "x"} {
		if _, _, err := VerifyMute(key, bad, w.now); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	if err := w.svc.Mute(t0(), person, kind); err != nil {
		t.Fatal(err)
	}
	if ch, _ := w.svc.Channel(t0(), "bob", ReviewRequested); ch != InApp {
		t.Fatalf("channel after mute: %s", ch)
	}
	w.notify(review)
	if got := w.deliver(); len(got) != 1 {
		t.Fatalf("muted mail still arrived: %d", len(got))
	}
	if ns, _ := w.svc.Store.List(t0(), "bob", Filter{}); len(ns) != 2 {
		t.Fatalf("a muted kind must still reach the inbox: %d", len(ns))
	}
	_ = link
}

func TestInboxReadDoneAndDedupe(t *testing.T) {
	w := newWorld(t)
	st := w.svc.Store
	e := review
	e.Dedupe = "once"
	w.notify(e)
	w.notify(e)
	ns, _ := st.List(t0(), "bob", Filter{State: "unread"})
	if len(ns) != 1 {
		t.Fatalf("dedupe failed: %d", len(ns))
	}
	if u, o, _ := st.Counts(t0(), "bob"); u != 1 || o != 1 {
		t.Fatalf("counts %d %d", u, o)
	}
	if n, err := st.MarkRead(t0(), "bob", []string{ns[0].ID}, true); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if n, _ := st.MarkRead(t0(), "mallory", []string{ns[0].ID}, false); n != 0 {
		t.Fatal("someone else's notice was changed")
	}
	if u, o, _ := st.Counts(t0(), "bob"); u != 0 || o != 1 {
		t.Fatalf("counts %d %d", u, o)
	}
	if _, err := st.MarkDone(t0(), "bob", []string{ns[0].ID}, true); err != nil {
		t.Fatal(err)
	}
	if open, _ := st.List(t0(), "bob", Filter{}); len(open) != 0 {
		t.Fatal("a done notice is still open")
	}
	if done, _ := st.List(t0(), "bob", Filter{State: "done"}); len(done) != 1 || !done[0].Read {
		t.Fatalf("%+v", done)
	}
	if _, err := st.List(t0(), "bob", Filter{Kind: "nonsense"}); err == nil {
		t.Fatal("a made-up kind filter was accepted")
	}
}

func TestQuietUntilOverMidnightAndInAZone(t *testing.T) {
	st := Settings{QuietStart: 22 * 60, QuietEnd: 7 * 60, Zone: "America/Chicago", DigestHour: 8}
	// 03:30 UTC is 21:30 the evening before in Chicago (CST, UTC-6): not quiet yet; 05:30 UTC is 23:30: quiet until 07:00 local = 13:00 UTC.
	if !st.QuietUntil(time.Date(2026, 11, 22, 3, 30, 0, 0, time.UTC)).IsZero() {
		t.Fatal("quiet too early")
	}
	end := st.QuietUntil(time.Date(2026, 11, 22, 5, 30, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 22, 13, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("quiet until %v, want %v", end, want)
	}
	if err := (Settings{QuietStart: 5, QuietEnd: -1, Zone: "UTC"}).Validate(); err == nil {
		t.Fatal("half a quiet window was accepted")
	}
	if err := (Settings{QuietStart: -1, QuietEnd: -1, Zone: "Mars/Olympus"}).Validate(); err == nil {
		t.Fatal("a made-up zone was accepted")
	}
}
