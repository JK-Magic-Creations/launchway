package githttp

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// The two services.
const (
	UploadPack  = "git-upload-pack"  // fetch and clone
	ReceivePack = "git-receive-pack" // push
)

// Backend is the repository storage as the transport uses it.
type Backend interface {
	Upload(ctx context.Context, repo string, rpc storage.RPC) error
	Receive(ctx context.Context, repo string, rpc storage.RPC) (storage.ReceiveResult, error)
}

// Options configure a Handler. Zero values are the defaults noted.
type Options struct {
	Backend Backend
	// MaxPushBytes caps a push request's body, compressed and decompressed
	// (default 2 GiB). MaxFetchBytes caps a fetch request's (default 32 MiB:
	// a request is only wants, haves and capabilities).
	MaxPushBytes, MaxFetchBytes int64
	// MaxConcurrent caps git processes serving requests at once (default 32);
	// a request waits up to QueueWait (default 30s) and is then refused with
	// 503. MaxPerUser is one person's share (default MaxConcurrent/2, at
	// least 1), beyond which a request is refused at once with 429.
	MaxConcurrent, MaxPerUser int
	QueueWait                 time.Duration
	// AdvertiseTimeout is the deadline of a ref advertisement (default 60s).
	// IdleTimeout is how long a transfer may make no progress either way
	// (default 5m): a large clone is legitimate, a stalled one is not.
	// MaxDuration is the longest any transfer may take (default 6h).
	AdvertiseTimeout, IdleTimeout, MaxDuration time.Duration
	Log                                        *slog.Logger
}

// Handler serves Git requests for repositories the caller has authorised.
type Handler struct {
	o    Options
	sem  chan struct{}
	mu   sync.Mutex
	user map[string]int
}

// New makes a Handler.
func New(o Options) *Handler {
	if o.MaxPushBytes <= 0 {
		o.MaxPushBytes = 2 << 30
	}
	if o.MaxFetchBytes <= 0 {
		o.MaxFetchBytes = 32 << 20
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 32
	}
	if o.MaxPerUser <= 0 {
		o.MaxPerUser = max(1, o.MaxConcurrent/2)
	}
	if o.QueueWait <= 0 {
		o.QueueWait = 30 * time.Second
	}
	if o.AdvertiseTimeout <= 0 {
		o.AdvertiseTimeout = 60 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 5 * time.Minute
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = 6 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Handler{o: o, sem: make(chan struct{}, o.MaxConcurrent), user: map[string]int{}}
}

// Request is what the caller has settled about a request.
type Request struct {
	Repo forge.Repository
	// Service is UploadPack or ReceivePack. Advertise says this is the GET of
	// the ref advertisement rather than the POST of the service.
	Service   string
	Advertise bool
	// User names who is calling, for the per-person cap.
	User string
}

// Result says what came of a request.
type Result struct {
	// Status is the HTTP status sent (200 when the transfer began).
	Status int
	// Receive is set for a push that ran.
	Receive *storage.ReceiveResult
	// Err is what went wrong, if anything (also when the client was told).
	Err error
}

// ErrTooLarge is a request body over its limit.
var ErrTooLarge = errors.New("the request is larger than this server accepts")

// Service reads the service a request is for from its path's last element
// (POST) or its service query parameter (the advertisement).
func Service(r *http.Request) (svc string, advertise bool, err error) {
	if r.Method == http.MethodGet {
		svc = r.URL.Query().Get("service")
		if svc != UploadPack && svc != ReceivePack {
			return "", true, errors.New("this server speaks the smart HTTP protocol only; update your git client (the dumb protocol is not served)")
		}
		return svc, true, nil
	}
	if i := strings.LastIndexByte(r.URL.Path, '/'); i >= 0 {
		svc = r.URL.Path[i+1:]
	}
	if svc != UploadPack && svc != ReceivePack {
		return "", false, errors.New("no such Git service")
	}
	return svc, false, nil
}

func (h *Handler) acquire(ctx context.Context, user string) (release func(), status int, msg string) {
	h.mu.Lock()
	if h.user[user] >= h.o.MaxPerUser {
		h.mu.Unlock()
		return nil, http.StatusTooManyRequests, fmt.Sprintf("you already have %d Git requests running; wait for one to finish", h.o.MaxPerUser)
	}
	h.user[user]++
	h.mu.Unlock()
	undo := func() {
		h.mu.Lock()
		if h.user[user]--; h.user[user] <= 0 {
			delete(h.user, user)
		}
		h.mu.Unlock()
	}
	t := time.NewTimer(h.o.QueueWait)
	defer t.Stop()
	select {
	case h.sem <- struct{}{}:
		return func() { <-h.sem; undo() }, 0, ""
	case <-t.C:
		undo()
		return nil, http.StatusServiceUnavailable, "this server is busy serving other Git requests; try again in a minute"
	case <-ctx.Done():
		undo()
		return nil, http.StatusServiceUnavailable, "the request ended while it waited"
	}
}

// Serve answers one request.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, q Request) Result {
	release, status, msg := h.acquire(r.Context(), q.User)
	if release == nil {
		w.Header().Set("Retry-After", "30")
		http.Error(w, msg, status)
		return Result{Status: status, Err: errors.New(msg)}
	}
	defer release()
	if !q.Advertise && !strings.EqualFold(r.Header.Get("Content-Type"), "application/x-"+q.Service+"-request") {
		http.Error(w, "the request is not a "+q.Service+" request", http.StatusUnsupportedMediaType)
		return Result{Status: http.StatusUnsupportedMediaType}
	}
	limit := h.o.MaxFetchBytes
	if q.Service == ReceivePack {
		limit = h.o.MaxPushBytes
	}
	if r.ContentLength > limit {
		return tooLarge(w, limit)
	}
	d := h.o.MaxDuration
	if q.Advertise {
		d = h.o.AdvertiseTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), d)
	defer cancel()
	rc := http.NewResponseController(w)
	out := &streamWriter{w: w, rc: rc, idle: h.o.IdleTimeout}
	var in io.Reader = &idleReader{r: http.MaxBytesReader(w, r.Body, limit), rc: rc, idle: h.o.IdleTimeout}
	if !q.Advertise && strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(in)
		if err != nil {
			http.Error(w, "the request body is not valid gzip", http.StatusBadRequest)
			return Result{Status: http.StatusBadRequest, Err: err}
		}
		defer gz.Close()
		in = &capReader{r: gz, left: limit} // a small body that inflates enormously is refused too
	}
	track := &trackReader{r: in}
	rpc := storage.RPC{Advertise: q.Advertise, In: track, Out: out, GitProtocol: r.Header.Get("Git-Protocol")}
	h2 := w.Header()
	h2.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	h2.Set("Pragma", "no-cache")
	h2.Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	if q.Advertise {
		h2.Set("Content-Type", "application/x-"+q.Service+"-advertisement")
		if q.Service != UploadPack || !strings.Contains(rpc.GitProtocol, "version=2") {
			pkt := "# service=" + q.Service + "\n"
			fmt.Fprintf(out, "%04x%s0000", len(pkt)+4, pkt)
		}
	} else {
		h2.Set("Content-Type", "application/x-"+q.Service+"-result")
	}
	var res Result
	var err error
	if q.Service == ReceivePack {
		var rr storage.ReceiveResult
		rr, err = h.o.Backend.Receive(ctx, q.Repo.ID, rpc)
		if !q.Advertise {
			res.Receive = &rr
		}
		var stale *storage.ErrStale
		if errors.As(err, &stale) {
			err = nil // the client was told in git's own words; this is not a server fault
		}
	} else {
		err = h.o.Backend.Upload(ctx, q.Repo.ID, rpc)
	}
	res.Status = http.StatusOK
	if err == nil {
		return res
	}
	// git only says its input ended badly: the reader knows why.
	var big *http.MaxBytesError
	if errors.As(track.err, &big) || errors.Is(track.err, ErrTooLarge) {
		err = fmt.Errorf("%w (%v)", ErrTooLarge, err)
	}
	res.Err = err
	h.failure(ctx, w, out, &res, err)
	return res
}

func tooLarge(w http.ResponseWriter, limit int64) Result {
	http.Error(w, fmt.Sprintf("the request is larger than the %d MiB this server accepts; push less at a time or ask the operator to raise forge.max_push_mib", limit>>20), http.StatusRequestEntityTooLarge)
	return Result{Status: http.StatusRequestEntityTooLarge, Err: ErrTooLarge}
}

// failure tells the client what it can still be told: if nothing was sent
// yet, a status and a sentence; otherwise the connection just ends, and
// git reports the truncated response.
func (h *Handler) failure(ctx context.Context, w http.ResponseWriter, out *streamWriter, res *Result, err error) {
	var tooBig *http.MaxBytesError
	status, msg := http.StatusInternalServerError, "something went wrong on the server; its log has the details"
	switch {
	case errors.As(err, &tooBig) || errors.Is(err, ErrTooLarge):
		status, msg = http.StatusRequestEntityTooLarge, "the request is larger than this server accepts"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		status, msg = http.StatusGatewayTimeout, "the request took too long"
	case errors.Is(err, context.Canceled):
		status, msg = 499, "the client went away"
	}
	if status == http.StatusInternalServerError {
		h.o.Log.Error("serving git", "err", err)
	}
	res.Status = status
	if out.n == 0 {
		w.Header().Del("Content-Type")
		http.Error(w, msg, status)
	}
}

// ---- readers and writers ----

// idleReader moves the connection's read deadline forward on every read, so
// a transfer that makes progress never times out and one that stalls does.
type idleReader struct {
	r    io.Reader
	rc   *http.ResponseController
	idle time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(i.idle))
	return i.r.Read(p)
}

// trackReader remembers the first error that is not the end of the input.
type trackReader struct {
	r   io.Reader
	err error
}

func (t *trackReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && err != io.EOF && t.err == nil {
		t.err = err
	}
	return n, err
}

// capReader fails with ErrTooLarge after left bytes.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var b [1]byte
		if n, _ := c.r.Read(b[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// streamWriter sends git's output as it is produced (so progress reaches the
// client), counts it, and keeps the write deadline moving.
type streamWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
	n    int64
}

func (s *streamWriter) Write(p []byte) (int, error) {
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.idle))
	n, err := s.w.Write(p)
	s.n += int64(n)
	if err == nil {
		_ = s.rc.Flush()
	}
	return n, err
}
