package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Authority identifies callers for the HTTP handler. Launchway implements it
// with its API tokens and roles (internal/api/registry_wire.go).
type Authority interface {
	// Authenticate checks a user name and secret sent as HTTP Basic: an API
	// token (the user name is free) or a run credential. A nil principal with
	// a nil error means the credentials are not accepted; the handler then
	// answers 401. Failures are the Authority's to count.
	Authenticate(r *http.Request, user, secret string) (*Principal, error)
	// Blocked answers 429 and returns true when the caller has failed too
	// often.
	Blocked(w http.ResponseWriter, r *http.Request) bool
	// Denied records a refused request (a push or delete someone may not do).
	Denied(r *http.Request, p *Principal, e Event)
	// Source is the address a request came from.
	Source(r *http.Request) string
}

// Handler serves the Distribution API under /v2/.
type Handler struct {
	Svc    *Service
	Auth   Authority
	Signer Signer
	// ServiceName is the "service" of the token challenge (default
	// launchway-registry).
	ServiceName string
	// Base returns the address clients reach this server at, without a path:
	// https://registry.example.com. Used for the token realm.
	Base func(r *http.Request) string
	// SecureEnough says whether a password may travel on this request (HTTPS,
	// a loopback listener, or a network the operator trusts).
	SecureEnough func(r *http.Request) bool
	// Limit counts requests per caller; nil: the Service's limit.
	limiter     *rateLimiter
	limiterOnce sync.Once
}

func (h *Handler) service() string {
	if h.ServiceName != "" {
		return h.ServiceName
	}
	return "launchway-registry"
}

var (
	reUploadStart = regexp.MustCompile(`^(.+)/blobs/uploads/?$`)
	reUpload      = regexp.MustCompile(`^(.+)/blobs/uploads/([^/]+)$`)
	reBlob        = regexp.MustCompile(`^(.+)/blobs/([^/]+)$`)
	reManifest    = regexp.MustCompile(`^(.+)/manifests/([^/]+)$`)
	reTags        = regexp.MustCompile(`^(.+)/tags/list$`)
	reReferrers   = regexp.MustCompile(`^(.+)/referrers/([^/]+)$`)
)

// IsRegistryPath reports whether a request path is the registry's.
func IsRegistryPath(p string) bool { return p == "/v2" || strings.HasPrefix(p, "/v2/") }

type rateLimiter struct {
	mu      sync.Mutex
	window  time.Time
	counts  map[string]int
	perMin  int
	nowFunc func() time.Time
}

func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	if l.perMin < 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFunc()
	if now.Sub(l.window) >= time.Minute || l.counts == nil {
		l.window, l.counts = now.Truncate(time.Minute), map[string]int{}
	}
	l.counts[key]++
	if l.counts[key] > l.perMin {
		return false, l.window.Add(time.Minute).Sub(now)
	}
	return true, 0
}

// ServeHTTP answers a request under /v2/.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(WithSource(r.Context(), h.Auth.Source(r)))
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	h.limiterOnce.Do(func() { h.limiter = &rateLimiter{perMin: h.Svc.limits.RequestsPerMinute, nowFunc: h.Svc.o.Now} })
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	path = strings.TrimPrefix(path, "/")
	if path == "token" {
		h.token(w, r)
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p != nil {
		if allowed, wait := h.limiter.allow(p.Subject); !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			h.fail(w, r, p, newErr(http.StatusTooManyRequests, CodeTooManyRequests, "too many requests from this caller; slow down", "Wait a moment and retry."))
			return
		}
	}
	switch path {
	case "":
		if p == nil {
			h.challenge(w, r, "", nil, "")
			return
		}
		writeOCIJSON(w, http.StatusOK, map[string]any{})
		return
	case "_catalog":
		h.catalog(w, r, p)
		return
	}
	if !strings.Contains(path, "/") {
		// /v2/<name> alone is not an endpoint.
		h.fail(w, r, p, newErr(http.StatusNotFound, CodeNameUnknown, "no such registry endpoint", ""))
		return
	}
	var name, arg string
	var kind string
	switch {
	case reUploadStart.MatchString(path):
		m := reUploadStart.FindStringSubmatch(path)
		name, kind = m[1], "upload-start"
	case reUpload.MatchString(path):
		m := reUpload.FindStringSubmatch(path)
		name, arg, kind = m[1], m[2], "upload"
	case reBlob.MatchString(path):
		m := reBlob.FindStringSubmatch(path)
		name, arg, kind = m[1], m[2], "blob"
	case reManifest.MatchString(path):
		m := reManifest.FindStringSubmatch(path)
		name, arg, kind = m[1], m[2], "manifest"
	case reTags.MatchString(path):
		name, kind = reTags.FindStringSubmatch(path)[1], "tags"
	case reReferrers.MatchString(path):
		m := reReferrers.FindStringSubmatch(path)
		name, arg, kind = m[1], m[2], "referrers"
	default:
		h.fail(w, r, p, newErr(http.StatusNotFound, CodeUnsupported, "no such registry endpoint", ""))
		return
	}
	if err := ValidName(name); err != nil {
		h.fail(w, r, p, newErr(http.StatusBadRequest, CodeNameInvalid, err.Error(), ""))
		return
	}
	action := Pull
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		action = Push
	case http.MethodDelete:
		action = Delete
		if kind == "upload" {
			action = Push // abandoning one's own upload is part of pushing
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE")
		h.fail(w, r, p, newErr(http.StatusMethodNotAllowed, CodeUnsupported, "this method is not supported here", ""))
		return
	}
	if p == nil {
		h.challenge(w, r, name, []Action{action}, "")
		return
	}
	if !h.allowed(r, p, name, action) {
		h.refuse(w, r, p, name, action)
		return
	}
	who := Who{Name: p.Subject, Run: p.Run}
	var err error
	switch kind {
	case "upload-start":
		if r.Method != http.MethodPost {
			err = newErr(http.StatusMethodNotAllowed, CodeUnsupported, "start an upload with POST", "")
			break
		}
		err = h.startUpload(w, r, p, name, who)
	case "upload":
		err = h.upload(w, r, name, arg, who)
	case "blob":
		err = h.blob(w, r, name, arg)
	case "manifest":
		err = h.manifest(w, r, name, arg, who)
	case "tags":
		err = h.tags(w, r, name)
	case "referrers":
		err = h.referrers(w, r, name, arg)
	}
	if err != nil {
		h.fail(w, r, p, err)
	}
}

// allowed says whether the caller may do an action to a repository.
func (h *Handler) allowed(r *http.Request, p *Principal, name string, a Action) bool {
	return p.Can(r.Context(), name, a)
}

// refuse answers a caller who may not do this. Someone who may not even pull
// is told nothing of whether the repository exists.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, p *Principal, name string, a Action) {
	if a != Pull && p.Can(r.Context(), name, Pull) {
		msg := fmt.Sprintf("you may pull %s but not %s to it", name, a)
		if p.Via == "run" {
			msg = fmt.Sprintf("this run's credential may not %s %s: a run pushes only to its own project's repositories, and never for a pull request or draft", a, name)
		}
		e := newErr(http.StatusForbidden, CodeDenied, msg, "Ask a project administrator for the operator role on "+ProjectOf(name)+".")
		h.Auth.Denied(r, p, Event{Type: string(a), Repo: name, Actor: p.Subject, Run: p.Run, Outcome: "denied", Detail: msg})
		h.fail(w, r, p, e)
		return
	}
	h.Auth.Denied(r, p, Event{Type: string(a), Repo: name, Actor: p.Subject, Run: p.Run, Outcome: "denied", Detail: "no access"})
	h.challenge(w, r, name, []Action{a}, "insufficient_scope")
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, p *Principal, err error) {
	e := asError(err)
	if e.Status >= 500 {
		h.Svc.log.Error("registry request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(e.Status)
		return
	}
	body := map[string]any{
		"errors": []map[string]any{{"code": e.Code, "message": e.Message, "detail": e.Detail}},
		"error":  e.Message, "code": strings.ToLower(e.Code),
	}
	if e.Next != "" {
		body["next"] = e.Next
	}
	writeOCIJSON(w, e.Status, body)
}

func writeOCIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// authenticate reads the Authorization header. It returns (nil, true) for a
// request with none (the caller then challenges), and false when it has
// already answered.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return nil, true
	}
	if h.Auth.Blocked(w, r) {
		return nil, false
	}
	scheme, rest, _ := strings.Cut(hdr, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(scheme) {
	case "bearer":
		if p, err := h.Signer.VerifyBearer(rest); err == nil {
			return p, true
		}
		// An expired token is the ordinary way a client learns to fetch a new one.
		h.challenge(w, r, "", nil, "invalid_token")
		return nil, false
	case "basic":
		user, pass, ok := basicCredentials(rest)
		if !ok {
			h.challenge(w, r, "", nil, "invalid_token")
			return nil, false
		}
		if h.SecureEnough != nil && !h.SecureEnough(r) {
			h.fail(w, r, nil, newErr(http.StatusForbidden, CodeDenied,
				"Launchway does not accept a password or token over plain HTTP; use HTTPS (or set registry.allow_insecure_http for a private network)", ""))
			return nil, false
		}
		p, err := h.Auth.Authenticate(r, user, pass)
		if err != nil {
			h.fail(w, r, nil, err)
			return nil, false
		}
		if p == nil {
			h.challenge(w, r, "", nil, "invalid_token")
			return nil, false
		}
		return p, true
	}
	h.challenge(w, r, "", nil, "invalid_token")
	return nil, false
}

func basicCredentials(b64 string) (user, pass string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(raw), ":")
	if !ok || pass == "" {
		pass = user
	}
	return user, pass, pass != ""
}

// challenge answers 401 with the Bearer challenge a client answers by asking
// the token endpoint, naming the scope it needs.
func (h *Handler) challenge(w http.ResponseWriter, r *http.Request, name string, actions []Action, errCode string) {
	base := ""
	if h.Base != nil {
		base = strings.TrimSuffix(h.Base(r), "/")
	}
	v := fmt.Sprintf(`Bearer realm="%s/v2/token",service="%s"`, base, h.service())
	if name != "" && len(actions) > 0 {
		var as []string
		for _, a := range actions {
			as = append(as, string(a))
		}
		v += fmt.Sprintf(`,scope="repository:%s:%s"`, name, strings.Join(as, ","))
	}
	if errCode != "" {
		v += fmt.Sprintf(`,error="%s"`, errCode)
	}
	w.Header().Set("WWW-Authenticate", v)
	h.fail(w, r, nil, newErr(http.StatusUnauthorized, CodeUnauthorized, "authentication required: sign in to the registry with an API token or a run credential", "Run: docker login (user: anything, password: a Launchway API token). Pipelines get credentials automatically."))
}

// token is the endpoint a client asks for a bearer token, with its Basic
// credentials. It grants only what the caller may do, from the scopes asked
// for; asking for more is not an error, the token just holds less.
func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		h.fail(w, r, nil, newErr(http.StatusMethodNotAllowed, CodeUnsupported, "ask for a token with GET", ""))
		return
	}
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="Launchway registry", charset="UTF-8"`)
		h.fail(w, r, nil, newErr(http.StatusUnauthorized, CodeUnauthorized, "authentication required: the registry has no anonymous access", "Run: docker login (user: anything, password: a Launchway API token)."))
		return
	}
	if h.Auth.Blocked(w, r) {
		return
	}
	scheme, rest, _ := strings.Cut(hdr, " ")
	user, pass, ok := basicCredentials(strings.TrimSpace(rest))
	if !strings.EqualFold(scheme, "basic") || !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Launchway registry", charset="UTF-8"`)
		h.fail(w, r, nil, newErr(http.StatusUnauthorized, CodeUnauthorized, "send your credentials as HTTP Basic", ""))
		return
	}
	if h.SecureEnough != nil && !h.SecureEnough(r) {
		h.fail(w, r, nil, newErr(http.StatusForbidden, CodeDenied, "Launchway does not accept a password or token over plain HTTP; use HTTPS", ""))
		return
	}
	p, err := h.Auth.Authenticate(r, user, pass)
	if err != nil {
		h.fail(w, r, nil, err)
		return
	}
	if p == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Launchway registry", charset="UTF-8"`)
		h.fail(w, r, nil, newErr(http.StatusUnauthorized, CodeUnauthorized, "these credentials are not accepted", "Create an API token and use it as the password."))
		return
	}
	if svc := r.URL.Query().Get("service"); svc != "" && svc != h.service() {
		h.fail(w, r, p, newErr(http.StatusBadRequest, CodeDenied, "this token service is "+h.service(), ""))
		return
	}
	access, err := h.grantScopes(r.Context(), r.URL.Query()["scope"], p)
	if err != nil {
		h.fail(w, r, p, err)
		return
	}
	tok, exp, err := h.Signer.IssueBearer(p, access, TokenTTL)
	if err != nil {
		h.fail(w, r, p, err)
		return
	}
	now := h.Svc.o.Now()
	writeOCIJSON(w, http.StatusOK, map[string]any{"token": tok, "access_token": tok, "expires_in": int(exp.Sub(now).Seconds()), "issued_at": now.UTC().Format(time.RFC3339)})
}

// grantScopes turns the scopes a client asked for into what the caller may
// do: each repository with the actions that caller is allowed. Asking for more
// is not an error; the token just holds less.
func (h *Handler) grantScopes(ctx context.Context, scopes []string, p *Principal) ([]Access, error) {
	var access []Access
	for _, sc := range scopes {
		for _, one := range strings.Fields(strings.ReplaceAll(sc, "+", " ")) {
			parts := strings.Split(one, ":")
			if len(parts) == 3 && parts[0] == "registry" && parts[1] == "catalog" {
				// The catalog lists what the caller may pull; a token carries
				// the first catalogLimit repositories of that list.
				repos, err := h.Svc.ListRepositories(ctx, "", func(ctx context.Context, n string) bool { return p.Can(ctx, n, Pull) })
				if err != nil {
					return nil, err
				}
				for i, repo := range repos {
					if i >= catalogLimit {
						break
					}
					access = append(access, Access{Name: repo.Name, Actions: []Action{Pull}})
				}
				continue
			}
			if len(parts) < 3 || parts[0] != "repository" {
				continue
			}
			// The name may not contain a colon; the actions are last.
			name := strings.Join(parts[1:len(parts)-1], ":")
			if ValidName(name) != nil {
				continue
			}
			access = append(access, Access{Name: name, Actions: allowedActions(ctx, p, name, strings.Split(parts[len(parts)-1], ","))})
		}
	}
	return access, nil
}

// allowedActions keeps of the asked actions those the caller may do ("*" asks for all).
func allowedActions(ctx context.Context, p *Principal, name string, asked []string) []Action {
	var granted []Action
	for _, a := range asked {
		act := Action(a)
		if act == "*" {
			for _, every := range []Action{Pull, Push, Delete} {
				if p.Can(ctx, name, every) {
					granted = append(granted, every)
				}
			}
			continue
		}
		if (act == Pull || act == Push || act == Delete) && p.Can(ctx, name, act) {
			granted = append(granted, act)
		}
	}
	return granted
}

// catalogLimit is how many repositories a bearer token made for the catalog
// names (a client that authenticates with Basic is not limited).
const catalogLimit = 500

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request, p *Principal) {
	if p == nil {
		h.challenge(w, r, "", nil, "")
		return
	}
	n, last := pageParams(r)
	repos, err := h.Svc.ListRepositories(r.Context(), "", func(ctx context.Context, name string) bool { return name > last && p.Can(ctx, name, Pull) })
	if err != nil {
		h.fail(w, r, p, err)
		return
	}
	names := []string{}
	more := false
	for _, repo := range repos {
		if n > 0 && len(names) == n {
			more = true
			break
		}
		names = append(names, repo.Name)
	}
	if more {
		w.Header().Set("Link", fmt.Sprintf(`</v2/_catalog?n=%d&last=%s>; rel="next"`, n, url.QueryEscape(names[len(names)-1])))
	}
	writeOCIJSON(w, http.StatusOK, map[string]any{"repositories": names})
}

func pageParams(r *http.Request) (n int, last string) {
	n, _ = strconv.Atoi(r.URL.Query().Get("n"))
	if n < 0 {
		n = 0
	}
	if n > 1000 {
		n = 1000
	}
	return n, r.URL.Query().Get("last")
}

func (h *Handler) tags(w http.ResponseWriter, r *http.Request, name string) error {
	n, last := pageParams(r)
	tags, more, err := h.Svc.ListTags(r.Context(), name, n, last)
	if err != nil {
		return err
	}
	if more {
		w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%d&last=%s>; rel="next"`, name, n, url.QueryEscape(tags[len(tags)-1])))
	}
	if r.Method == http.MethodHead {
		return nil
	}
	writeOCIJSON(w, http.StatusOK, map[string]any{"name": name, "tags": tags})
	return nil
}

func (h *Handler) referrers(w http.ResponseWriter, r *http.Request, name, digest string) error {
	if err := ValidDigest(digest); err != nil {
		return newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
	}
	if _, err := h.Svc.repoRow(r.Context(), name); err != nil {
		return err
	}
	filter := r.URL.Query().Get("artifactType")
	ms, err := h.Svc.Referrers(r.Context(), name, digest, filter)
	if err != nil {
		return err
	}
	manifests := []map[string]any{}
	for _, m := range ms {
		d := map[string]any{"mediaType": m.MediaType, "digest": m.Digest, "size": m.Size}
		if m.ArtifactType != "" {
			d["artifactType"] = m.ArtifactType
		}
		if ann := userAnnotations(m.Annotations); len(ann) > 0 {
			d["annotations"] = ann
		}
		manifests = append(manifests, d)
	}
	if filter != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	w.Header().Set("Content-Type", MediaOCIIndex)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	return json.NewEncoder(w).Encode(map[string]any{"schemaVersion": 2, "mediaType": MediaOCIIndex, "manifests": manifests})
}

func userAnnotations(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k == AnnPlatforms || k == AnnTotalSize {
			continue
		}
		out[k] = v
	}
	return out
}

// ---- blobs and uploads ----

func (h *Handler) blob(w http.ResponseWriter, r *http.Request, name, digest string) error {
	if r.Method == http.MethodDelete {
		return newErr(http.StatusMethodNotAllowed, CodeUnsupported, "blobs are not deleted one by one: delete the image and run garbage collection", "")
	}
	if err := ValidDigest(digest); err != nil {
		return newErr(http.StatusBadRequest, CodeDigestInvalid, err.Error(), "")
	}
	b, err := h.Svc.BlobInfo(r.Context(), name, digest)
	if err != nil {
		return err
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Etag", `"`+digest+`"`)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	start, length, partial, err := parseRange(r.Header.Get("Range"), b.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", b.Size))
		return &Error{Status: http.StatusRequestedRangeNotSatisfiable, Code: CodeRangeInvalid, Message: err.Error()}
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, b.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead || length == 0 {
		return nil
	}
	rc, err := h.Svc.OpenBlob(r.Context(), b, start, length)
	if err != nil {
		return nil // the status is sent; the connection ends short and the client's digest check fails
	}
	defer rc.Close()
	_, _ = io.Copy(w, io.LimitReader(rc, length))
	return nil
}

// parseRange reads a single "bytes=a-b", "bytes=a-" or "bytes=-n" range.
func parseRange(h string, size int64) (start, length int64, partial bool, err error) {
	if h == "" {
		return 0, size, false, nil
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, size, false, nil // not a range we serve: send the whole blob
	}
	a, b, _ := strings.Cut(spec, "-")
	switch a {
	case "":
		n, e := strconv.ParseInt(b, 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, false, errors.New("the range is not satisfiable")
		}
		if n > size {
			n = size
		}
		return size - n, n, true, nil
	default:
		s, e := strconv.ParseInt(a, 10, 64)
		if e != nil || s < 0 || s >= size {
			if size == 0 && s == 0 {
				return 0, 0, false, nil
			}
			return 0, 0, false, errors.New("the range starts past the end of the blob")
		}
		end := size - 1
		if b != "" {
			if v, e := strconv.ParseInt(b, 10, 64); e == nil && v < end {
				end = v
			}
		}
		if end < s {
			return 0, 0, false, errors.New("the range is not satisfiable")
		}
		return s, end - s + 1, true, nil
	}
}

func uploadLocation(name, id string) string { return "/v2/" + name + "/blobs/uploads/" + id }

func (h *Handler) uploadHeaders(w http.ResponseWriter, name string, u Upload) {
	w.Header().Set("Location", uploadLocation(name, u.ID))
	w.Header().Set("Docker-Upload-UUID", u.ID)
	end := u.Size - 1
	if end < 0 {
		end = 0
	}
	w.Header().Set("Range", fmt.Sprintf("0-%d", end))
	w.Header().Set("OCI-Chunk-Min-Length", "0")
}

func (h *Handler) startUpload(w http.ResponseWriter, r *http.Request, p *Principal, name string, who Who) error {
	q := r.URL.Query()
	if mount := q.Get("mount"); mount != "" {
		if from := q.Get("from"); from != "" && ValidName(from) == nil && p.Can(r.Context(), from, Pull) {
			ok, err := h.Svc.MountBlob(r.Context(), name, from, mount, who.Name)
			if err != nil {
				return err
			}
			if ok {
				w.Header().Set("Location", "/v2/"+name+"/blobs/"+mount)
				w.Header().Set("Docker-Content-Digest", mount)
				w.WriteHeader(http.StatusCreated)
				return nil
			}
		}
		// Nothing to mount (or no right to): upload as usual.
	}
	if d := q.Get("digest"); d != "" { // a monolithic upload in one request
		b, err := h.Svc.PutBlobDirect(r.Context(), name, d, r.Body, who.Name)
		if err != nil {
			return err
		}
		w.Header().Set("Location", "/v2/"+name+"/blobs/"+b.Digest)
		w.Header().Set("Docker-Content-Digest", b.Digest)
		w.WriteHeader(http.StatusCreated)
		return nil
	}
	u, err := h.Svc.StartUpload(r.Context(), name, who.Name)
	if err != nil {
		return err
	}
	h.uploadHeaders(w, name, u)
	w.WriteHeader(http.StatusAccepted)
	return nil
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, name, id string, who Who) error {
	u, err := h.Svc.GetUpload(r.Context(), name, id)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		h.uploadHeaders(w, name, u)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		start := int64(-1)
		if cr := r.Header.Get("Content-Range"); cr != "" {
			a, _, _ := strings.Cut(strings.TrimPrefix(cr, "bytes "), "-")
			if v, err := strconv.ParseInt(a, 10, 64); err == nil {
				start = v
			}
		}
		u, err = h.Svc.AppendChunk(r.Context(), u, start, r.Body)
		if err != nil {
			return err
		}
		h.uploadHeaders(w, name, u)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodPut:
		d := r.URL.Query().Get("digest")
		if d == "" {
			return newErr(http.StatusBadRequest, CodeDigestInvalid, "finish an upload with ?digest=sha256:...", "")
		}
		b, err := h.Svc.FinishUpload(r.Context(), u, d, r.Body, who.Name)
		if err != nil {
			return err
		}
		w.Header().Set("Location", "/v2/"+name+"/blobs/"+b.Digest)
		w.Header().Set("Docker-Content-Digest", b.Digest)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		_ = h.Svc.CancelUpload(r.Context(), u)
		w.WriteHeader(http.StatusNoContent)
	default:
		return newErr(http.StatusMethodNotAllowed, CodeUnsupported, "this method is not supported on an upload", "")
	}
	return nil
}

// ---- manifests ----

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request, name, ref string, who Who) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		m, body, err := h.Svc.ResolveManifest(r.Context(), name, ref, true)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", m.MediaType)
		w.Header().Set("Docker-Content-Digest", m.Digest)
		w.Header().Set("Etag", `"`+m.Digest+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body) //nolint:gosec // G705: a manifest is served with its own JSON media type, never as HTML, and with nosniff
		}
	case http.MethodPut:
		limit := h.Svc.limits.MaxManifestBytes
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			return err
		}
		ct := r.Header.Get("Content-Type")
		if i := strings.Index(ct, ";"); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
		digest, err := h.Svc.PutManifest(r.Context(), name, ref, ct, body, who)
		if err != nil {
			return err
		}
		w.Header().Set("Location", "/v2/"+name+"/manifests/"+digest)
		w.Header().Set("Docker-Content-Digest", digest)
		var doc struct {
			Subject *struct {
				Digest string `json:"digest"`
			} `json:"subject"`
		}
		if json.Unmarshal(body, &doc) == nil && doc.Subject != nil && doc.Subject.Digest != "" {
			w.Header().Set("OCI-Subject", doc.Subject.Digest)
		}
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if IsDigest(ref) {
			if _, err := h.Svc.DeleteManifest(r.Context(), name, ref, who); err != nil {
				return err
			}
		} else if err := h.Svc.DeleteTag(r.Context(), name, ref, who); err != nil {
			return err
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		return newErr(http.StatusMethodNotAllowed, CodeUnsupported, "this method is not supported on a manifest", "")
	}
	return nil
}
