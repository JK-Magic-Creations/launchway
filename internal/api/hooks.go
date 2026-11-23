package api

// Schedules and webhooks.
//
// Two kinds of webhook reach the server, both without a session or API
// token: they prove themselves with a secret the server's configuration
// names (an environment variable or a file; never the database).
//
//   - A Git host's push and pull-request webhooks (GitHub, Gitea, Forgejo,
//     GitLab), at /api/v1/projects/{name}/git-hook. Each is checked the way
//     its host signs, and only makes the server poll the project at once:
//     the poller stays the source of truth, so a forged or replayed call
//     could at worst cause a poll. Deliveries are remembered by their ID,
//     and a repeat is answered but ignored.
//   - A pipeline's webhook triggers, at
//     /api/v1/projects/{name}/webhooks/{trigger}, which start a run with
//     inputs mapped from the JSON body. The caller sends a bearer token, or
//     signs the body with HMAC-SHA256 over "<timestamp>.<body>" with a
//     timestamp within five minutes; a signed request is accepted once.
//     An Idempotency-Key header makes a repeated call answer with the run
//     the first one started.
//
// A caller that cannot prove itself learns nothing: an unknown project, a
// project without the hook, and a wrong secret get the same answer.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Limits on webhook calls.
const (
	maxGitHookBody     = 25 << 20 // GitHub's own limit on a payload
	maxTriggerBody     = 64 << 10
	maxIdempotencyKey  = 200
	signatureTolerance = 5 * time.Minute
)

// projectSchedules lists a project's schedules, as read from its default
// branch, with when each fires next and recent firings, and its webhooks.
func (s *Server) projectSchedules(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	v, err := s.Projects.Schedules(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// notFound is what every caller that cannot prove itself is told.
func hookNotFound(w http.ResponseWriter) { writeErr(w, http.StatusNotFound, "not found") }

// hookDenied counts a failed webhook call against its source and records
// it, unless the source is already refused.
func (s *Server) hookDenied(r *http.Request, action, who, why string) {
	addr, now := s.clientAddr(r), s.Now()
	l := s.limiter()
	wasBlocked := l.blocked(addr, now) > 0
	l.fail(addr, now)
	if wasBlocked {
		return
	}
	s.record(&engine.AuditEntry{At: now.UnixMilli(), User: who, Source: addr, RequestID: reqID(r), Action: action, Project: r.PathValue("name"),
		Outcome: "denied", Detail: why})
}

// ---- Git hosts ----

// gitHook checks a Git host's webhook call and polls the project at once.
func (s *Server) gitHook(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	name := r.PathValue("name")
	var hook *source.GitHook
	if s.Projects != nil {
		hook = s.Projects.Hook(name)
	}
	var secret []byte
	if hook != nil {
		var err error
		if secret, err = hook.Secret.Read(); err != nil {
			s.Log.Error("reading a Flight's Git webhook secret", "project", name, "err", err)
			hook = nil
		}
	}
	kind := "github"
	if hook != nil {
		kind = hook.Kind
	} else {
		secret = []byte("no hook: compare anyway, so the answer takes as long")
	}
	ok, delivery, event, key, err := verifyGitHook(kind, secret, r)
	if errors.As(err, new(*http.MaxBytesError)) {
		writeErr(w, http.StatusRequestEntityTooLarge, "the body is larger than this server accepts")
		return
	}
	if hook == nil || !ok {
		s.hookDenied(r, "webhook.git", "git:"+kind, "a Git webhook call that could not prove itself")
		hookNotFound(w)
		return
	}
	who := "git:" + kind
	{
		// A replay is told by what the verification covers (the body and the event), not by the host's delivery id,
		// which is not signed: a captured delivery under a fresh id is the same delivery. The id stays in the audit line.
		fresh, err := s.Engine.SeeDelivery(name, kind, key)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !fresh {
			// Signed, but seen before: a replay. It changes nothing.
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "already received"})
			return
		}
	}
	if !gitEventPolls(event) {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "event": event})
		return
	}
	s.Projects.PollNow(name)
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: who, Source: s.clientAddr(r), RequestID: reqID(r), Action: "webhook.git", Project: name,
		Target: clip(delivery, maxTarget), Outcome: "ok", Detail: "event " + clip(event, 60) + ": polling the Flight now"})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "polling"})
}

// gitEventPolls reports whether a Git host's event may have moved a ref.
func gitEventPolls(event string) bool {
	switch strings.ToLower(event) {
	case "push", "pull_request", "create", "delete", "push hook", "tag push hook", "merge request hook", "system hook":
		return true
	}
	return false
}

// verifyGitHook checks a call the way the given host signs it, reading the
// body as it goes, and returns the delivery's ID and event.
func verifyGitHook(kind string, secret []byte, r *http.Request) (ok bool, delivery, event, key string, err error) {
	h := r.Header
	sum := sha256.New()
	body := http.MaxBytesReader(nil, r.Body, maxGitHookBody)
	r.Body = io.NopCloser(io.TeeReader(body, sum))
	defer func() {
		if err == nil {
			_, err = io.Copy(io.Discard, r.Body) // what the check did not read (a token check reads none) is hashed too
		}
		sum.Write([]byte{0})
		sum.Write([]byte(strings.ToLower(event)))
		key = "body-" + hex.EncodeToString(sum.Sum(nil))
	}()
	switch kind {
	case "gitlab":
		// GitLab sends the secret token itself, not a signature.
		got := h.Get("X-Gitlab-Token")
		ok = got != "" && subtle.ConstantTimeCompare([]byte(got), secret) == 1
		delivery = h.Get("X-Gitlab-Event-UUID")
		if delivery == "" {
			delivery = h.Get("X-Gitlab-Webhook-UUID")
		}
		return ok, delivery, h.Get("X-Gitlab-Event"), "", nil
	case "gitea", "forgejo":
		sig := h.Get("X-Forgejo-Signature")
		if sig == "" {
			sig = h.Get("X-Gitea-Signature")
		}
		delivery = h.Get("X-Forgejo-Delivery")
		if delivery == "" {
			delivery = h.Get("X-Gitea-Delivery")
		}
		event = h.Get("X-Forgejo-Event")
		if event == "" {
			event = h.Get("X-Gitea-Event")
		}
		ok, err = bodyMACMatches(secret, r, "", sig)
		return ok, delivery, event, "", err
	default: // github
		ok, err = bodyMACMatches(secret, r, "sha256=", h.Get("X-Hub-Signature-256"))
		return ok, h.Get("X-GitHub-Delivery"), h.Get("X-GitHub-Event"), "", err
	}
}

// bodyMACMatches reports whether sig is prefix and the hex HMAC-SHA256 of
// the request's body. The body is hashed as it is read, never held whole.
func bodyMACMatches(secret []byte, r *http.Request, prefix, sig string) (bool, error) {
	mac := hmac.New(sha256.New, secret)
	if _, err := io.Copy(mac, r.Body); err != nil {
		return false, err
	}
	return macEqual(mac, prefix, sig), nil
}

func macEqual(mac hash.Hash, prefix, sig string) bool {
	hexSig, ok := strings.CutPrefix(sig, prefix)
	if !ok || hexSig == "" {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	return err == nil && hmac.Equal(got, mac.Sum(nil))
}

// ---- webhook triggers ----

// webhookTrigger starts the run a pipeline's webhook trigger asks for.
func (s *Server) webhookTrigger(w http.ResponseWriter, r *http.Request) {
	if s.tooManyFailures(w, r) {
		return
	}
	project, name := r.PathValue("name"), r.PathValue("trigger")
	if r.ContentLength > maxTriggerBody {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d KiB", maxTriggerBody>>10))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTriggerBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than %d KiB", maxTriggerBody>>10))
		return
	}
	var a *source.TriggerAuth
	if s.Projects != nil && pipeline.ValidWebhookName(name) {
		a = s.Projects.TriggerAuth(project, name)
	}
	who := "webhook:" + clip(name, 63)
	signature, why := s.checkTrigger(a, r, body)
	if why != "" {
		s.hookDenied(r, "webhook.run", who, why)
		hookNotFound(w)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > maxIdempotencyKey || strings.ContainsFunc(key, func(c rune) bool { return c < 0x21 || c > 0x7e }) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("Idempotency-Key is up to %d printable characters", maxIdempotencyKey))
		return
	}
	if signature != "" {
		// A signed request is accepted once: its signature names it.
		fresh, err := s.Engine.SeeDelivery(project, "signed:"+name, signature)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !fresh && key == "" {
			writeErr(w, http.StatusConflict, "this signed request was already received; sign a new one (or send an Idempotency-Key to get the first call's run)")
			return
		}
	}
	sum := sha256.Sum256(body)
	entry := &engine.AuditEntry{At: s.Now().UnixMilli(), User: who, Source: s.clientAddr(r), RequestID: reqID(r), Action: "webhook.run", Project: project}
	run, repeat, err := s.Projects.StartWebhook(r.Context(), source.WebhookRequest{Project: project, Name: name, Body: body,
		Key: key, BodyHash: hex.EncodeToString(sum[:])})
	if err != nil {
		entry.Outcome, entry.Detail = "failed", err.Error()
		s.record(entry)
		s.fail(w, err)
		return
	}
	entry.Outcome, entry.Target = "ok", run.ID
	entry.Detail = fmt.Sprintf("run #%d of %s at %s", run.Number, nonEmpty(run.Branch, run.Tag), shortSHA(run.SHA))
	if repeat {
		entry.Detail = "a repeat with the same Idempotency-Key: " + entry.Detail
		w.Header().Set("Idempotent-Replayed", "true")
	} else if d, err := s.Engine.GetRun(run.ID); err == nil && d.TriggerDetail != nil {
		entry.Detail += inputsDetail(d.TriggerDetail.Inputs)
	}
	s.record(entry)
	code := http.StatusCreated
	if repeat {
		code = http.StatusOK
	}
	writeJSON(w, code, run)
}

// checkTrigger checks that a call proves what the trigger's configuration
// asks: a bearer token, or a fresh HMAC signature. It returns the
// signature of a signed call, or why the call is refused.
func (s *Server) checkTrigger(a *source.TriggerAuth, r *http.Request, body []byte) (signature, why string) {
	if a == nil {
		// Compare anyway, so that the answer takes as long.
		subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer no-such-trigger-configured"))
		return "", "a call to a webhook the server's configuration does not list"
	}
	if !a.UsesHMAC() {
		secret, err := a.Token.Read()
		if err != nil {
			s.Log.Error("reading a webhook trigger's token", "project", r.PathValue("name"), "trigger", r.PathValue("trigger"), "err", err)
			return "", "the server could not read the trigger's token"
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), secret) != 1 {
			return "", "a wrong or missing token"
		}
		return "", ""
	}
	secret, err := a.HMAC.Read()
	if err != nil {
		s.Log.Error("reading a webhook trigger's secret", "project", r.PathValue("name"), "trigger", r.PathValue("trigger"), "err", err)
		return "", "the server could not read the trigger's secret"
	}
	ts := r.Header.Get("X-Launchway-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", "no X-Launchway-Timestamp"
	}
	if d := s.Now().Sub(time.Unix(sec, 0)); d > signatureTolerance || d < -signatureTolerance {
		return "", "a signature whose timestamp is more than five minutes away"
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := r.Header.Get("X-Launchway-Signature")
	if !macEqual(mac, "sha256=", sig) {
		return "", "a wrong or missing signature"
	}
	return strings.ToLower(strings.TrimPrefix(sig, "sha256=")), ""
}
