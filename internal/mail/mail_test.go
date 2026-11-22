package mail

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/mail/smtptest"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type secrets map[string]string

func (s secrets) Lookup(_ context.Context, _ string, r pipeline.SecretRef) (string, error) {
	v, ok := s[r.Key]
	if !ok {
		return "", io.ErrUnexpectedEOF
	}
	return v, nil
}

func ref(k string) *pipeline.SecretRef { return &pipeline.SecretRef{Store: "env", Key: k} }

func setup(t *testing.T) (*smtptest.Server, *Sender) {
	t.Helper()
	srv, err := smtptest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.User, srv.Pass = "mailer", "s3cret-pass"
	return srv, &Sender{Config: Config{Host: srv.Host, Port: srv.Port, From: "Launchway <notify@example.com>", Username: ref("u"), Password: ref("p"), TLSConfig: srv.ClientTLS()},
		Secrets: secrets{"u": "mailer", "p": "s3cret-pass"}}
}

func msg() Message {
	return Message{To: "dana@example.com", Subject: "[acme/shop] review requested", Text: "Open https://launchway.example.com/pulls/1\n", HTML: "<p>Open <a href=\"https://launchway.example.com/pulls/1\">the pull request</a></p>"}
}

func TestSendsOverStartTLSAndSignsInOnlyAfterEncryption(t *testing.T) {
	srv, s := setup(t)
	if err := s.Send(t.Context(), msg()); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || !got[0].TLS || got[0].User != "mailer" || got[0].To != "dana@example.com" || got[0].From != "notify@example.com" {
		t.Fatalf("%+v", got)
	}
	d := got[0].Data
	for _, want := range []string{"multipart/alternative", "text/plain", "text/html", "Subject: [acme/shop] review requested", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(d, want) {
			t.Errorf("the message lacks %q:\n%s", want, d)
		}
	}
	for _, remote := range []string{"<img", "<link", "<script", "src=", "url(", "@import"} {
		if strings.Contains(strings.ToLower(d), remote) {
			t.Errorf("the message refers to remote content (%s)", remote)
		}
	}
	if srv.PlainMail() != 0 {
		t.Fatal("mail was offered before encryption")
	}
}

func TestRefusesAServerWithoutEncryptionUnlessAllowed(t *testing.T) {
	srv, s := setup(t)
	srv.NoSTARTTLS = true
	err := s.Send(t.Context(), msg())
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want a refusal naming STARTTLS: %v", err)
	}
	if len(srv.Messages()) != 0 || srv.PlainMail() != 0 {
		t.Fatalf("something was sent in the clear: %+v plain %d", srv.Messages(), srv.PlainMail())
	}
	// Plain is only for a relay the administrator vouches for.
	s.Config.Security = None
	if err := s.Config.Check(); err == nil || !strings.Contains(err.Error(), "allow_plain") {
		t.Fatalf("security none must need allow_plain: %v", err)
	}
	s.Config.AllowPlain = true
	srv.RequireTLS, srv.User, s.Config.Username, s.Config.Password = false, "", nil, nil
	if err := s.Send(t.Context(), msg()); err != nil {
		t.Fatal(err)
	}
	if got := srv.Messages(); len(got) != 1 || got[0].TLS {
		t.Fatalf("%+v", got)
	}
}

func TestImplicitTLSAndWrongPassword(t *testing.T) {
	srv, s := setup(t)
	srv.Implicit = true
	s.Config.Security = TLS
	if err := s.Send(t.Context(), msg()); err != nil || len(srv.Messages()) != 1 {
		t.Fatalf("%v %+v", err, srv.Messages())
	}
	s.Secrets = secrets{"u": "mailer", "p": "wrong"}
	err := s.Send(t.Context(), msg())
	if err == nil || strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("a bad sign-in must fail without echoing a password: %v", err)
	}
	s.Secrets = secrets{}
	if err := s.Send(t.Context(), msg()); err == nil || !strings.Contains(err.Error(), "secret store") {
		t.Fatalf("an unreadable credential: %v", err)
	}
}

func TestTheMailServerPassesTheEgressGuard(t *testing.T) {
	srv, s := setup(t)
	g := egress.New(egress.Policy{})
	s.Dial = g.Dialer(5 * time.Second)
	err := s.Send(t.Context(), msg())
	if err == nil || len(srv.Messages()) != 0 {
		t.Fatalf("a mail server on a loopback address must be refused unless egress.allow lists it: %v", err)
	}
	if err := g.Set(egress.Policy{Allow: []string{"127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(t.Context(), msg()); err != nil || len(srv.Messages()) != 1 {
		t.Fatalf("listed in egress.allow: %v", err)
	}
}

func TestHeadersCannotBeInjected(t *testing.T) {
	srv, s := setup(t)
	m := msg()
	m.Subject = "hello\r\nBcc: eve@example.com"
	m.Headers = map[string]string{"X-Evil": "a\r\nBcc: eve@example.com"}
	if err := s.Send(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	d := srv.Messages()[0].Data
	if strings.Contains(d, "\r\nBcc:") || strings.Contains(d, "X-Evil") {
		t.Fatalf("injected header:\n%s", d)
	}
	m.To = "dana@example.com\r\nRCPT TO:<eve@example.com>"
	if err := s.Send(t.Context(), m); err == nil {
		t.Fatal("a recipient with a line break was accepted")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newQueue(t *testing.T) (*Queue, *smtptest.Server, *fakeClock, *[]string) {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	srv, s := setup(t)
	clk := &fakeClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	var audit []string
	q := &Queue{DB: eng.Database(), Sender: s, Now: clk.Now, Backoff: func(int) time.Duration { return time.Minute },
		Audit: func(_ context.Context, a, person, detail, outcome string) {
			audit = append(audit, a+"|"+person+"|"+detail+"|"+outcome)
		}}
	return q, srv, clk, &audit
}

func item(person, subject string) Item {
	return Item{Person: person, To: person + "@example.com", Kind: "review_requested", Subject: subject, Text: "body of " + subject, HTML: "<p>body</p>"}
}

func TestQueueRetriesThenSendsAndForgetsTheBody(t *testing.T) {
	q, srv, clk, audit := newQueue(t)
	srv.FailNext(2)
	id, err := q.Enqueue(t.Context(), item("dana", "first"))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, 0, 1} {
		got, err := q.RunOnce(t.Context())
		if err != nil || got != want {
			t.Fatalf("pass %d: sent %d (want %d) %v", i, got, want, err)
		}
		if i < 2 {
			if again, _ := q.RunOnce(t.Context()); again != 0 {
				t.Fatalf("pass %d: tried again before the backoff", i)
			}
			clk.Advance(time.Minute)
		}
	}
	state, attempts, text, _, err := store.MailState(t.Context(), q.DB, id)
	if err != nil || state != "sent" || attempts != 3 || text != "" {
		t.Fatalf("%s %d %q %v", state, attempts, text, err)
	}
	if len(*audit) != 3 || !strings.HasPrefix((*audit)[0], "mail.retry|dana|") || !strings.HasPrefix((*audit)[2], "mail.sent|dana|") {
		t.Fatalf("audit: %v", *audit)
	}
	for _, a := range *audit {
		if strings.Contains(a, "first") || strings.Contains(a, "body of") || strings.Contains(a, "@example.com") {
			t.Fatalf("the audit holds the subject, body or address: %s", a)
		}
	}
}

func TestQueueGivesUpAfterTheLimitAndStaysBounded(t *testing.T) {
	q, srv, clk, audit := newQueue(t)
	q.MaxAttempts, q.Max = 2, 3
	srv.FailNext(10)
	id, _ := q.Enqueue(t.Context(), item("dana", "doomed"))
	for i := 0; i < 2; i++ {
		_, _ = q.RunOnce(t.Context())
		clk.Advance(time.Minute)
	}
	state, _, text, lastErr, err := store.MailState(t.Context(), q.DB, id)
	if err != nil || state != "failed" || text != "" || lastErr == "" {
		t.Fatalf("%s %q %q %v", state, text, lastErr, err)
	}
	if last := (*audit)[len(*audit)-1]; !strings.HasPrefix(last, "mail.failed|dana|") {
		t.Fatalf("%v", *audit)
	}
	for i := 0; i < 3; i++ {
		if _, err := q.Enqueue(t.Context(), item("eve", "x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Enqueue(t.Context(), item("eve", "one too many")); err != ErrQueueFull {
		t.Fatalf("the queue grew without bound: %v", err)
	}
}

func TestInstallationRateLimitHoldsMessagesBack(t *testing.T) {
	q, srv, clk, _ := newQueue(t)
	q.PerMinute, q.PerHour = 2, 3
	for _, n := range []string{"a", "b", "c", "d"} {
		if _, err := q.Enqueue(t.Context(), item(n, n)); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := q.RunOnce(t.Context()); n != 2 {
		t.Fatalf("first pass sent %d, want 2 (per minute)", n)
	}
	clk.Advance(61 * time.Second)
	if n, _ := q.RunOnce(t.Context()); n != 1 {
		t.Fatalf("second pass sent %d, want 1 (the hour's third)", n)
	}
	clk.Advance(time.Hour)
	if n, _ := q.RunOnce(t.Context()); n != 1 || len(srv.Messages()) != 4 {
		t.Fatalf("third pass sent %d, %d in all", n, len(srv.Messages()))
	}
}

func TestNotBeforeHoldsAMessage(t *testing.T) {
	q, srv, clk, _ := newQueue(t)
	it := item("dana", "later")
	it.NotBefore = clk.Now().Add(2 * time.Hour)
	if _, err := q.Enqueue(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.RunOnce(t.Context()); n != 0 {
		t.Fatal("sent during quiet hours")
	}
	clk.Advance(2*time.Hour + time.Second)
	if n, _ := q.RunOnce(t.Context()); n != 1 || len(srv.Messages()) != 1 {
		t.Fatalf("sent %d", n)
	}
}

func TestSendTest(t *testing.T) {
	q, srv, _, _ := newQueue(t)
	if err := q.SendTest(t.Context(), "admin@example.com"); err != nil || len(srv.Messages()) != 1 || !strings.Contains(srv.Messages()[0].Data, "mail settings work") {
		t.Fatalf("%v", err)
	}
}
