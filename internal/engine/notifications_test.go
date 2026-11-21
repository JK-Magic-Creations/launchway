package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/notify"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Lookup(_ context.Context, _ string, ref pipeline.SecretRef) (string, error) {
	if v, ok := f[ref.Key]; ok {
		return v, nil
	}
	return "", io.ErrUnexpectedEOF
}

type receiver struct {
	mu   sync.Mutex
	got  []string
	sigs []string
	code int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got, r.sigs = append(r.got, string(b)), append(r.sigs, req.Header.Get("X-Launchway-Signature-256"))
	if r.code != 0 {
		w.WriteHeader(r.code)
	}
}

func (r *receiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

func notifyEngine(t *testing.T, cfg notify.Config) (*Engine, *Notifier) {
	t.Helper()
	e, _ := notifyEngineClock(t, cfg)
	return e, &Notifier{BaseURL: "https://ci.example.com", Client: http.DefaultClient,
		Secrets: fakeSecrets{"hook-url": "", "signing": "s3cret"}}
}

func notifyEngineClock(t *testing.T, cfg notify.Config) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	if err := e.Reconfigure(LiveOptions{Notifications: cfg}); err != nil {
		t.Fatal(err)
	}
	return e, clk
}

func channelsFor(t *testing.T, n *Notifier, url string, kinds ...string) notify.Config {
	n.Secrets = fakeSecrets{"hook-url": url, "signing": "s3cret"}
	var cfg notify.Config
	for _, k := range kinds {
		ch := notify.Channel{Name: k, Kind: k, URL: pipeline.SecretRef{Store: "vault", Key: "hook-url"}}
		if k == notify.Webhook {
			ch.Secret = &pipeline.SecretRef{Store: "vault", Key: "signing"}
		}
		cfg.Channels = append(cfg.Channels, ch)
	}
	return cfg
}

// An approval that waits sends one message, with the run's link, to every
// channel its rule names, signed for the webhook; the next look sends no more.
func TestApprovalWaitingIsToldOnce(t *testing.T) {
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	_, n := notifyEngine(t, notify.Config{})
	cfg := channelsFor(t, n, srv.URL+"/hooks/xyz-token", notify.Webhook, notify.Slack)
	cfg.Rules = []notify.Rule{{On: []string{notify.ApprovalRequested}, To: []string{notify.Webhook, notify.Slack}}}
	e, _ := notifyEngine(t, cfg)
	toApproval(t, e)
	ctx := context.Background()
	e.notifyOnce(ctx, n)
	if rec.count() != 2 {
		t.Fatalf("%d messages, want one for each channel: %v", rec.count(), rec.got)
	}
	var hook, chat string
	for i, b := range rec.got {
		if strings.Contains(b, `"event"`) {
			hook = b
			if rec.sigs[i] != signBody("s3cret", []byte(b)) {
				t.Errorf("the webhook is not signed: %q", rec.sigs[i])
			}
		} else {
			chat = b
		}
	}
	for _, want := range []string{`"event":"approval.requested"`, `waits for you at approve`, `https://ci.example.com/runs/run_`, `step=approve`} {
		if !strings.Contains(hook, want) {
			t.Errorf("the webhook body lacks %s: %s", want, hook)
		}
	}
	if !strings.HasPrefix(chat, `{"text":"Run #1 of demo waits for you at approve`) {
		t.Errorf("slack body: %s", chat)
	}
	e.notifyOnce(ctx, n)
	e.notifyOnce(ctx, n)
	if rec.count() != 2 {
		t.Fatalf("the same approval was sent again: %d messages", rec.count())
	}
	log, err := e.Notifications(10, nil)
	if err != nil || len(log) != 2 || log[0].Status != "sent" || log[0].Attempts != 1 {
		t.Fatalf("log: %v %+v", err, log)
	}
	for _, l := range log {
		if strings.Contains(l.Title+l.LastError, "xyz-token") {
			t.Fatal("the channel's address is in the log")
		}
	}
}

// A channel that does not answer is tried again later, and the log says why
// without showing its address; rules that do not match send nothing.
func TestFailedDeliveryIsRetriedAndLogged(t *testing.T) {
	rec := &receiver{code: 500}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	_, n := notifyEngine(t, notify.Config{})
	cfg := channelsFor(t, n, srv.URL+"/hooks/xyz-token", notify.Discord)
	cfg.Rules = []notify.Rule{
		{On: []string{notify.RunFailed}, Projects: []string{"demo"}, Branches: []string{"main"}, To: []string{notify.Discord}},
		{On: []string{notify.RunFailed}, Projects: []string{"other"}, To: []string{notify.Discord}},
	}
	e, _ := notifyEngine(t, cfg)
	ctx := context.Background()
	e.notifyOnce(ctx, n) // sets where to start reading
	id := create(t, e, gated, onMain)
	fail(t, e, take(t, e))
	e.notifyOnce(ctx, n)
	log, _ := e.Notifications(10, nil)
	if len(log) != 1 || log[0].Event != "run.failed" || log[0].Status != "pending" || log[0].Attempts != 1 ||
		log[0].LastError != "the channel answered 500 Internal Server Error" || log[0].NextAt == 0 {
		t.Fatalf("log: %+v", log)
	}
	if !strings.Contains(rec.got[0], `"content":"Run #1 of demo failed`) || !strings.Contains(rec.got[0], "/runs/"+id) {
		t.Fatalf("body: %s", rec.got[0])
	}
	// Not due yet: nothing is sent. Due, and the channel works again: sent.
	e.notifyOnce(ctx, n)
	if rec.count() != 1 {
		t.Fatalf("retried at once: %d", rec.count())
	}
	rec.mu.Lock()
	rec.code = 0
	rec.mu.Unlock()
	if _, err := e.db.Exec(`UPDATE notifications SET next_at = 0`); err != nil {
		t.Fatal(err)
	}
	e.notifyOnce(ctx, n)
	log, _ = e.Notifications(10, nil)
	if rec.count() != 2 || log[0].Status != "sent" || log[0].Attempts != 2 {
		t.Fatalf("after the retry: %d %+v", rec.count(), log)
	}
	// The project filter: the other project's rule did not send this one, and a project the caller cannot read is hidden.
	if l, _ := e.Notifications(10, func(string) bool { return false }); len(l) != 0 {
		t.Fatalf("hidden rows were listed: %+v", l)
	}
}

// A step that waits too long is told again, once, after the rule's time.
func TestWaitingTooLong(t *testing.T) {
	rec := &receiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	_, n := notifyEngine(t, notify.Config{})
	cfg := channelsFor(t, n, srv.URL, notify.Teams)
	cfg.Rules = []notify.Rule{{On: []string{notify.StepWaitingLong}, After: "1h", To: []string{notify.Teams}}}
	e, clk := notifyEngineClock(t, cfg)
	toApproval(t, e)
	e.notifyOnce(context.Background(), n)
	if rec.count() != 0 {
		t.Fatalf("told before the time: %v", rec.got)
	}
	clk.Add(2 * time.Hour)
	e.notifyOnce(context.Background(), n)
	e.notifyOnce(context.Background(), n)
	if rec.count() != 1 || !strings.Contains(rec.got[0], "has waited for you at approve") {
		t.Fatalf("%d messages: %v", rec.count(), rec.got)
	}
}
