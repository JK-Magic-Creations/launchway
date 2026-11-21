package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 for receivers of the older header only; the SHA-256 header is sent too
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/verify"
)

// Options configure a Service. Zero values are the defaults noted.
type Options struct {
	DB     DB
	Sealer Sealer
	// Client sends the requests; it must refuse addresses the installation
	// does not allow (egress.Guard.HTTPClient).
	Client *http.Client
	Log    *slog.Logger
	// Timeout is how long one request may take (10s).
	Timeout time.Duration
	// MaxAttempts is how many times a delivery is tried (5); Backoff is the wait
	// before each retry (10s, 1m, 5m, 30m).
	MaxAttempts int
	Backoff     []time.Duration
	// DisableAfter is how many deliveries in a row must fail (after their
	// retries) before the hook is disabled (5).
	DisableAfter int
	// KeepDeliveries is how many deliveries a hook's log keeps (200).
	KeepDeliveries int
	// OnDisabled is told when a hook was disabled, with the notice.
	OnDisabled func(h Hook, reason string)
	// UserAgent identifies the sender ("Launchway-Hookshot").
	UserAgent string
	Now       func() time.Time
}

// Service manages hooks and delivers events.
type Service struct {
	o    Options
	st   *store
	wake chan struct{}
}

// New makes a Service.
func New(o Options) *Service {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if len(o.Backoff) == 0 {
		o.Backoff = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute}
	}
	if o.DisableAfter <= 0 {
		o.DisableAfter = 5
	}
	if o.KeepDeliveries <= 0 {
		o.KeepDeliveries = 200
	}
	if o.UserAgent == "" {
		o.UserAgent = "Launchway-Hookshot"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: o.Timeout}
	}
	return &Service{o: o, st: &store{db: o.DB, seal: o.Sealer, now: o.Now}, wake: make(chan struct{}, 1)}
}

// NewHook is a hook to create.
type NewHook struct {
	RepoID    string // a repository hook
	Owner     string // an owner hook: every repository of this owner
	Name      string
	URL       string
	Events    []string
	Active    *bool
	Secret    string // empty: one is made and returned once
	CreatedBy string
}

// ErrInvalid is a hook that cannot be accepted; its text says why.
var ErrInvalid = errors.New("invalid webhook")

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

// Create adds a hook. The secret is returned only when Launchway made it.
func (s *Service) Create(ctx context.Context, n NewHook) (h Hook, generated string, err error) {
	if (n.RepoID == "") == (n.Owner == "") {
		return h, "", invalid("a hook belongs to one repository or to one owner")
	}
	if msg := CheckURL(n.URL); msg != "" {
		return h, "", invalid(msg)
	}
	ev, msg := Normalise(n.Events)
	if msg != "" {
		return h, "", invalid(msg)
	}
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = MaskURL(n.URL) // the full address may carry a token: it is not the default name
	}
	if len(name) > 100 {
		return h, "", invalid("the name is longer than 100 characters")
	}
	secret := n.Secret
	if secret == "" {
		secret = randomSecret()
		generated = secret
	}
	active := n.Active == nil || *n.Active
	h, err = s.st.insertHook(ctx, Hook{RepoID: n.RepoID, Owner: n.Owner, Name: name, URL: strings.TrimSpace(n.URL), Events: ev, Active: active, CreatedBy: n.CreatedBy}, secret)
	if err != nil {
		return h, "", err
	}
	h.URL = MaskURL(h.URL)
	h.Scope = ScopeOwner
	if h.RepoID != "" {
		h.Scope = ScopeRepository
	}
	return h, generated, nil
}

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Get returns a hook.
func (s *Service) Get(ctx context.Context, id string) (Hook, error) {
	h, _, err := s.st.hook(ctx, id)
	h.URL = MaskURL(h.URL)
	return h, err
}

// ForRepository lists the hooks of one repository.
func (s *Service) ForRepository(ctx context.Context, repoID string) ([]Hook, error) {
	return masked(s.st.hooks(ctx, `repo_id = ?`, repoID))
}

// ForOwner lists the hooks that cover an owner's repositories.
func (s *Service) ForOwner(ctx context.Context, owner string) ([]Hook, error) {
	return masked(s.st.hooks(ctx, `repo_id = '' AND owner = ?`, owner))
}

// masked hides the path of each hook's address (see MaskURL).
func masked(hs []Hook, err error) ([]Hook, error) {
	for i := range hs {
		hs[i].URL = MaskURL(hs[i].URL)
	}
	return hs, err
}

// Patch is a change to a hook; nil fields stay.
type Patch struct {
	Name   *string
	URL    *string
	Events []string
	Active *bool
	Secret *string
}

// Update changes a hook. Turning an automatically disabled hook on again
// clears its failures.
func (s *Service) Update(ctx context.Context, id string, p Patch) (Hook, error) {
	h, _, err := s.st.hook(ctx, id)
	if err != nil {
		return h, err
	}
	if p.Name != nil {
		if h.Name = strings.TrimSpace(*p.Name); h.Name == "" || len(h.Name) > 100 {
			return h, invalid("the name must be 1 to 100 characters")
		}
	}
	if p.URL != nil {
		if msg := CheckURL(*p.URL); msg != "" {
			return h, invalid(msg)
		}
		h.URL = strings.TrimSpace(*p.URL)
	}
	if p.Events != nil {
		ev, msg := Normalise(p.Events)
		if msg != "" {
			return h, invalid(msg)
		}
		h.Events = ev
	}
	if p.Active != nil {
		if *p.Active && !h.Active {
			h.Failures, h.DisabledReason, h.DisabledAt = 0, "", time.Time{}
		}
		h.Active = *p.Active
	}
	if err := s.st.saveHook(ctx, h, p.Secret); err != nil {
		return h, err
	}
	if p.Secret != nil {
		h.HasSecret = *p.Secret != ""
	}
	h.URL = MaskURL(h.URL)
	return h, nil
}

// Delete removes a hook and its log.
func (s *Service) Delete(ctx context.Context, id string) error { return s.st.deleteHook(ctx, id) }

// Deliveries returns the newest deliveries of a hook.
func (s *Service) Deliveries(ctx context.Context, hookID string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.st.deliveries(ctx, hookID, limit)
}

// Delivery returns one delivery.
func (s *Service) Delivery(ctx context.Context, id string) (Delivery, error) {
	return s.st.delivery(ctx, id)
}

// Emit queues an event for every active hook that wants it: the repository's
// own hooks and its owner's. The payload is built once per hook (it carries
// the delivery's ID).
func (s *Service) Emit(ctx context.Context, repoID, owner, event string, p Payload) {
	hooks, err := s.st.hooks(ctx, `active = 1 AND (repo_id = ? OR (repo_id = '' AND owner = ?))`, repoID, owner)
	if err != nil {
		s.o.Log.Error("finding the webhooks of an event", "err", err)
		return
	}
	queued := false
	for _, h := range hooks {
		if !h.Wants(event) {
			continue
		}
		if _, err := s.queue(ctx, h, event, p, false, ""); err != nil {
			s.o.Log.Error("queuing a webhook delivery", "hook", h.ID, "err", err)
			continue
		}
		queued = true
	}
	if queued {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) queue(ctx context.Context, h Hook, event string, p Payload, test bool, redeliveryOf string) (Delivery, error) {
	d := Delivery{HookID: h.ID, Event: event, Test: test, RedeliveryOf: redeliveryOf}
	d.ID = newDeliveryID()
	p.Event, p.Delivery, p.At = event, d.ID, s.o.Now().UTC()
	raw, err := json.Marshal(p)
	if err != nil {
		return d, err
	}
	d.Payload = raw
	return s.st.insertDelivery(ctx, d)
}

// Ping sends a test delivery now, one attempt, and returns what came of it.
func (s *Service) Ping(ctx context.Context, hookID string, repo Repo, by string) (Delivery, error) {
	h, _, err := s.st.hook(ctx, hookID)
	if err != nil {
		return Delivery{}, err
	}
	d, err := s.queue(ctx, h, EventPing, Payload{Repository: repo, Sender: Sender{Name: by}, Zen: "Launchway can reach this address."}, true, "")
	if err != nil {
		return d, err
	}
	return s.attemptNow(ctx, d.ID)
}

// Redeliver sends a delivery's request again, now, once, as a new delivery.
func (s *Service) Redeliver(ctx context.Context, deliveryID string) (Delivery, error) {
	old, err := s.st.delivery(ctx, deliveryID)
	if err != nil {
		return old, err
	}
	h, _, err := s.st.hook(ctx, old.HookID)
	if err != nil {
		return old, err
	}
	d, err := s.st.insertDelivery(ctx, Delivery{HookID: h.ID, Event: old.Event, Payload: old.Payload, Test: old.Test, RedeliveryOf: old.ID})
	if err != nil {
		return d, err
	}
	return s.attemptNow(ctx, d.ID)
}

// attemptNow claims a delivery and tries it once, whatever its attempts so far;
// a failure leaves it failed (a person asked, so there are no retries).
func (s *Service) attemptNow(ctx context.Context, id string) (Delivery, error) {
	if ok, err := s.st.claim(ctx, id, s.o.Now()); err != nil || !ok {
		d, _ := s.st.delivery(ctx, id)
		return d, err
	}
	return s.attempt(ctx, id, true)
}

// Run delivers until ctx ends. Every server runs it; a delivery is claimed by
// one.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		}
	}
}

// Sweep makes the attempts that are due.
func (s *Service) Sweep(ctx context.Context) {
	ids, err := s.st.due(ctx, s.o.Now(), 50)
	if err != nil {
		s.o.Log.Warn("looking for webhook deliveries that are due", "err", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, id := range ids {
		if ok, err := s.st.claim(ctx, id, s.o.Now()); err != nil || !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.attempt(ctx, id, false); err != nil {
				s.o.Log.Warn("a webhook delivery", "err", err)
			}
		}()
	}
	wg.Wait()
}

// Sign is the signature of a body under a secret, as sent in
// X-Launchway-Signature-256.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// SignSHA1 is the HMAC-SHA1 signature of a body, as sent in X-Hub-Signature for
// receivers that do not read the SHA-256 one. It is the same secret and the same
// keyed construction; the stronger header is always sent beside it.
func SignSHA1(secret string, body []byte) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write(body)
	return "sha1=" + hex.EncodeToString(m.Sum(nil))
}

func newDeliveryID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// attempt sends a claimed delivery and records the outcome. once says that
// no retry follows a failure.
func (s *Service) attempt(ctx context.Context, id string, once bool) (Delivery, error) {
	d, err := s.st.delivery(ctx, id)
	if err != nil {
		return d, err
	}
	h, sealed, err := s.st.hook(ctx, d.HookID)
	if err != nil {
		d.State, d.Error = Failed, "the webhook was deleted"
		_ = s.st.recordAttempt(ctx, d)
		return d, nil
	}
	if !h.Active && !d.Test && d.RedeliveryOf == "" {
		d.State, d.Error = Failed, "the webhook is off: "+h.DisabledReason
		_ = s.st.recordAttempt(ctx, d)
		return d, nil
	}
	secret := ""
	if sealed != "" {
		if b, err := s.o.Sealer.Open(ctx, h.ID, sealed); err != nil {
			d.Error = "the secret could not be opened with this server's key"
		} else {
			secret = string(b)
		}
	}
	status, took, body, sendErr := s.send(ctx, h, d, secret)
	d.Attempts++
	d.Status, d.DurationMS, d.Response = status, took.Milliseconds(), clip(redactFor(h.URL, body), 1024)
	switch {
	case sendErr == nil && status >= 200 && status < 300:
		d.State, d.Error = Delivered, ""
		_, _ = s.st.hookFailures(ctx, h.ID, 0, true)
	default:
		d.Error = redactFor(h.URL, failureText(status, sendErr))
		if once || d.Attempts >= s.o.MaxAttempts {
			d.State = Failed
			s.noteFailure(ctx, h, d)
		} else {
			d.State = Pending
			d.NextAttempt = s.o.Now().Add(s.o.Backoff[min(d.Attempts-1, len(s.o.Backoff)-1)])
		}
	}
	if err := s.st.recordAttempt(ctx, d); err != nil {
		return d, err
	}
	s.st.prune(ctx, h.ID, s.o.KeepDeliveries)
	return d, nil
}

// redactFor removes what must not be stored from text that came back from a
// hook's address (or from the connection to it): the address's own path and
// query (which may be the token), then the product's redaction of token-like
// strings and Authorization and Cookie values. The response of a chat service
// often echoes the address or a token; the log shows it only redacted.
func redactFor(address, text string) string {
	if u, err := url.Parse(address); err == nil {
		for _, v := range []string{u.RawQuery, u.EscapedPath(), u.Path} {
			if len(strings.Trim(v, "/")) >= 4 {
				text = strings.ReplaceAll(text, v, "...")
			}
		}
		if len(address) >= 4 {
			text = strings.ReplaceAll(text, address, MaskURL(address))
		}
	}
	return verify.Redact(text)
}

func failureText(status int, err error) string {
	switch {
	case err != nil && strings.Contains(err.Error(), "not allowed"):
		return "Launchway may not connect to that address: it is an internal address and the installation does not allow it (egress.allow). " + err.Error()
	case err != nil:
		return clip(err.Error(), 300)
	case status >= 300 && status < 400:
		return fmt.Sprintf("the address answered with a redirect (%d); redirects are not followed: use the final address", status)
	default:
		return fmt.Sprintf("the address answered %d; a status from 200 to 299 is a success", status)
	}
}

// noteFailure counts a delivery that failed for good and disables the hook
// when enough have in a row.
func (s *Service) noteFailure(ctx context.Context, h Hook, d Delivery) {
	if d.Test || d.RedeliveryOf != "" {
		return // a person tried it by hand: it says so on the screen, and it does not count against the hook
	}
	n, err := s.st.hookFailures(ctx, h.ID, 1, false)
	if err != nil || n < s.o.DisableAfter || !h.Active {
		return
	}
	h.Active, h.Failures, h.DisabledAt = false, n, s.o.Now().UTC()
	h.DisabledReason = fmt.Sprintf("Turned off automatically after %d deliveries in a row failed. The last problem: %s Fix the address, then turn the webhook on again.", n, d.Error)
	if err := s.st.saveHook(ctx, h, nil); err != nil {
		return
	}
	if s.o.OnDisabled != nil {
		s.o.OnDisabled(h, h.DisabledReason)
	}
}

func (s *Service) send(ctx context.Context, h Hook, d Delivery, secret string) (status int, took time.Duration, body string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(d.Payload))
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.o.UserAgent)
	req.Header.Set("X-Launchway-Event", d.Event)
	req.Header.Set("X-Launchway-Delivery", d.ID)
	// GitHub's name for the event, for receivers written for its format.
	req.Header.Set("X-GitHub-Event", d.Event)
	if secret != "" {
		sig := Sign(secret, d.Payload)
		req.Header.Set("X-Launchway-Signature-256", sig)
		req.Header.Set("X-Hub-Signature-256", sig)
		// Receivers written for the older header (a GitOps controller built on an earlier
		// version of the library that reads GitHub's events) read only the HMAC-SHA1 one.
		req.Header.Set("X-Hub-Signature", SignSHA1(secret, d.Payload))
	}
	start := time.Now()
	resp, err := s.o.Client.Do(req)
	took = time.Since(start)
	if err != nil {
		return 0, took, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, took, string(b), nil
}
