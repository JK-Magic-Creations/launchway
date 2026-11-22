package blobstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// Distinctive credentials, so a test can tell if one leaks into an error.
var fakeCreds = S3Credentials{AccessKeyID: "AKIDLEAKCHECK0000001", SecretAccessKey: "secret/LEAKCHECK+do-not-print-0000000001"}

const fakeBucket = "fake-bucket"

// fakeS3 is a small in-memory S3 server. It checks every request's
// signature the way S3 does (recomputing it from what arrived), the
// payload hash and Content-MD5, and implements the operations the store
// uses, including EntityTooSmall and InvalidPart on completion.
type fakeS3 struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]*fakeUpload
	nextID  int

	// hook, when set, sees every correctly signed request first and may
	// answer it instead (return true).
	hook     func(w http.ResponseWriter, r *http.Request, body []byte) bool
	requests atomic.Int64
}

type fakeUpload struct {
	key   string
	parts map[int][]byte
}

func newFakeS3(t *testing.T) *fakeS3 {
	f := &fakeS3{t: t, objects: map[string][]byte{}, uploads: map[string]*fakeUpload{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// store returns an S3 store pointed at the fake, with short retry waits.
func (f *fakeS3) store(t *testing.T, prefix string) *S3 {
	t.Helper()
	s, err := NewS3(S3Config{Endpoint: f.srv.URL, Bucket: fakeBucket, Prefix: prefix}, fakeCreds)
	must(t, err)
	s.retryBase = time.Millisecond
	return s
}

var authRE = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/]+)/(\d{8})/([a-z0-9-]+)/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

func s3XMLError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>REQ123</RequestId></Error>`, code, msg)
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s3XMLError(w, 400, "IncompleteBody", "could not read the body")
		return
	}
	if msg := f.checkSignature(r, body); msg != "" {
		s3XMLError(w, 403, "SignatureDoesNotMatch", msg)
		return
	}
	if f.hook != nil && f.hook(w, r, body) {
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+fakeBucket)
	if !ok {
		s3XMLError(w, 404, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	key := strings.TrimPrefix(rest, "/")
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "POST" && q.Has("uploads"):
		f.nextID++
		// IDs with characters that need escaping in a query string.
		id := fmt.Sprintf("up/%d+x=y", f.nextID)
		f.uploads[id] = &fakeUpload{key: key, parts: map[int][]byte{}}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, fakeBucket, key, xmlEscape(id))
	case r.Method == "PUT" && q.Has("uploadId"):
		up := f.uploads[q.Get("uploadId")]
		if up == nil || up.key != key {
			s3XMLError(w, 404, "NoSuchUpload", "The specified upload does not exist.")
			return
		}
		if r.Header.Get("Content-MD5") == "" {
			s3XMLError(w, 400, "InvalidRequest", "UploadPart without Content-MD5")
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		up.parts[n] = body
		w.Header().Set("ETag", `"`+md5hex(body)+`"`)
	case r.Method == "POST" && q.Has("uploadId"):
		up := f.uploads[q.Get("uploadId")]
		if up == nil || up.key != key {
			s3XMLError(w, 404, "NoSuchUpload", "The specified upload does not exist.")
			return
		}
		var doc struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(body, &doc); err != nil || len(doc.Parts) == 0 {
			s3XMLError(w, 400, "MalformedXML", "bad completion document")
			return
		}
		var whole []byte
		for i, p := range doc.Parts {
			data, ok := up.parts[p.PartNumber]
			if !ok || `"`+md5hex(data)+`"` != p.ETag {
				s3XMLError(w, 400, "InvalidPart", "One or more of the specified parts could not be found.")
				return
			}
			if i < len(doc.Parts)-1 && len(data) < MinPartSize {
				s3XMLError(w, 400, "EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.")
				return
			}
			whole = append(whole, data...)
		}
		f.objects[key] = whole
		delete(f.uploads, q.Get("uploadId"))
		fmt.Fprintf(w, `<CompleteMultipartUploadResult><Key>%s</Key><ETag>"x-%d"</ETag></CompleteMultipartUploadResult>`, key, len(doc.Parts))
	case r.Method == "DELETE" && q.Has("uploadId"):
		if f.uploads[q.Get("uploadId")] == nil {
			s3XMLError(w, 404, "NoSuchUpload", "The specified upload does not exist.")
			return
		}
		delete(f.uploads, q.Get("uploadId"))
		w.WriteHeader(204)
	case r.Method == "PUT":
		if key == "" {
			w.WriteHeader(200) // create bucket
			return
		}
		f.objects[key] = body
		w.Header().Set("ETag", `"`+md5hex(body)+`"`)
	case r.Method == "GET", r.Method == "HEAD":
		data, ok := f.objects[key]
		if !ok {
			if r.Method == "HEAD" {
				w.WriteHeader(404)
				return
			}
			s3XMLError(w, 404, "NoSuchKey", "The specified key does not exist.")
			return
		}
		status := 200
		if rg := r.Header.Get("Range"); rg != "" {
			from, to, ok := parseRange(rg, int64(len(data)))
			if !ok {
				s3XMLError(w, 416, "InvalidRange", "The requested range is not satisfiable")
				return
			}
			data, status = data[from:to], 206
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(status)
		if r.Method == "GET" {
			w.Write(data)
		}
	case r.Method == "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	default:
		s3XMLError(w, 400, "NotImplemented", r.Method+" "+r.URL.RawQuery)
	}
}

// checkSignature recomputes the request's signature from what arrived,
// with the secret the fake knows, and returns what is wrong, if anything.
func (f *fakeS3) checkSignature(r *http.Request, body []byte) string {
	m := authRE.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		return "missing or malformed Authorization header: " + r.Header.Get("Authorization")
	}
	if m[1] != fakeCreds.AccessKeyID {
		return "unknown access key"
	}
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hexSHA256(body) {
		return "x-amz-content-sha256 does not match the body"
	}
	if md := r.Header.Get("Content-MD5"); md != "" {
		sum := md5.Sum(body)
		if md != base64.StdEncoding.EncodeToString(sum[:]) {
			return "Content-MD5 does not match the body"
		}
	}
	when, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil || when.Format("20060102") != m[2] {
		return "bad X-Amz-Date"
	}
	signed := strings.Split(m[4], ";")
	if !sort.StringsAreSorted(signed) || !contains(signed, "host") || !contains(signed, "x-amz-date") || !contains(signed, "x-amz-content-sha256") {
		return "SignedHeaders lacks host, x-amz-date or x-amz-content-sha256: " + m[4]
	}
	// Rebuild the request as S3 sees it, with only the signed headers.
	clone, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	clone.Host = r.Host
	for _, h := range signed {
		if h != "host" && h != "x-amz-date" {
			clone.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}
	sg := signer{creds: S3Credentials{AccessKeyID: m[1], SecretAccessKey: fakeCreds.SecretAccessKey, SessionToken: r.Header.Get("X-Amz-Security-Token")}, region: m[3], service: "s3"}
	got := sg.sign(clone, hexSHA256(body), when)
	if got.signature != m[5] {
		return "The request signature we calculated does not match the signature you provided."
	}
	return ""
}

func parseRange(h string, size int64) (int64, int64, bool) {
	spec, ok := strings.CutPrefix(h, "bytes=")
	a, b, ok2 := strings.Cut(spec, "-")
	if !ok || !ok2 {
		return 0, 0, false
	}
	from, err := strconv.ParseInt(a, 10, 64)
	if err != nil || from >= size {
		return 0, 0, false
	}
	to := size
	if b != "" {
		end, err := strconv.ParseInt(b, 10, 64)
		if err != nil || end < from {
			return 0, 0, false
		}
		to = min(end+1, size)
	}
	return from, to, true
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// assertNoCredentials fails if err mentions any of the fake credentials.
func assertNoCredentials(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, c := range []string{fakeCreds.AccessKeyID, fakeCreds.SecretAccessKey} {
		if strings.Contains(err.Error(), c) {
			t.Errorf("the error %q contains a credential", err)
		}
	}
}

func TestS3FakeConformance(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "launchway/")
	testStore(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.objects {
		if !strings.HasPrefix(k, "launchway/") {
			t.Errorf("object %q was written outside the prefix", k)
		}
		t.Errorf("object %q was left behind", k)
	}
}

func TestS3RetriesUnavailable(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	putObject(t, s, "obj", []byte("data"))
	var calls atomic.Int64
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if calls.Add(1) == 1 {
			s3XMLError(w, 503, "SlowDown", "Please reduce your request rate.")
			return true
		}
		return false
	}
	size, err := s.Stat(ctx, "obj")
	if err != nil || size != 4 {
		t.Fatalf("Stat after a 503: %d, %v", size, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d attempts, want 2", calls.Load())
	}

	// Every retryable status is retried; four attempts in all, then the
	// last error is returned.
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		calls.Store(0)
		f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
			calls.Add(1)
			s3XMLError(w, status, "Busy", "try later")
			return true
		}
		_, err := s.Stat(ctx, "obj")
		var se *S3Error
		if !errors.As(err, &se) || se.StatusCode != status || calls.Load() != 4 {
			t.Errorf("status %d: %d attempts, error %v", status, calls.Load(), err)
		}
	}
}

func TestS3DoesNotRetryClientErrors(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	for _, tc := range []struct {
		status int
		code   string
	}{{403, "AccessDenied"}, {400, "InvalidArgument"}, {404, "NoSuchBucket"}, {409, "OperationAborted"}} {
		var calls atomic.Int64
		f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
			calls.Add(1)
			// A misbehaving proxy echoing the credentials back.
			s3XMLError(w, tc.status, tc.code, "Denied for "+fakeCreds.AccessKeyID+" with "+fakeCreds.SecretAccessKey)
			return true
		}
		_, err := s.CreateUpload(ctx, "k")
		var se *S3Error
		if !errors.As(err, &se) || se.Code != tc.code || se.StatusCode != tc.status {
			t.Errorf("%d: got %v, want an S3Error with code %s", tc.status, err, tc.code)
		}
		if calls.Load() != 1 {
			t.Errorf("%d %s was tried %d times, want once", tc.status, tc.code, calls.Load())
		}
		if errors.Is(err, ErrNotFound) {
			t.Errorf("%s matches ErrNotFound", tc.code)
		}
		if !strings.Contains(err.Error(), tc.code) || !strings.Contains(err.Error(), "REQ123") {
			t.Errorf("the error %q does not name the code and request ID", err)
		}
		assertNoCredentials(t, err)
	}
}

func TestS3CompleteErrorIn200(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	key := "in200"
	id, err := s.CreateUpload(ctx, key)
	must(t, err)
	etag, err := s.UploadPart(ctx, key, id, 1, []byte("part"))
	must(t, err)
	parts := []Part{{Number: 1, ETag: etag}}

	// A non-retryable error in a 200 answer is an error, not success.
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if r.Method == "POST" && r.URL.Query().Has("uploadId") {
			w.WriteHeader(200)
			io.WriteString(w, "<?xml version=\"1.0\"?>\n\n   <Error><Code>InvalidPart</Code><Message>part gone</Message><RequestId>R</RequestId></Error>")
			return true
		}
		return false
	}
	err = s.CompleteUpload(ctx, key, id, parts)
	var se *S3Error
	if !errors.As(err, &se) || se.Code != "InvalidPart" || se.StatusCode != 200 {
		t.Fatalf("got %v, want InvalidPart", err)
	}

	// InternalError in a 200 answer is retried.
	var calls atomic.Int64
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if r.Method == "POST" && r.URL.Query().Has("uploadId") && calls.Add(1) == 1 {
			w.WriteHeader(200)
			io.WriteString(w, "<Error><Code>InternalError</Code><Message>We encountered an internal error. Please try again.</Message></Error>")
			return true
		}
		return false
	}
	must(t, s.CompleteUpload(ctx, key, id, parts))
	if calls.Load() != 2 {
		t.Fatalf("%d completion attempts, want 2", calls.Load())
	}
	if got := readAll(t, s, key, 0, -1); string(got) != "part" {
		t.Fatalf("got %q", got)
	}
	// Completing again: S3 no longer knows the upload, the object exists.
	f.hook = nil
	must(t, s.CompleteUpload(ctx, key, id, parts))
}

func TestS3NotFound(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "p")
	if _, err := s.Open(ctx, "missing", 0, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Open(ctx, "missing", 3, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open with a range: %v", err)
	}
	if _, err := s.Stat(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat: %v", err)
	}
	if _, err := s.UploadPart(ctx, "missing", "nope", 1, []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UploadPart: %v", err)
	}
	must(t, s.AbortUpload(ctx, "missing", "nope"))
	must(t, s.Delete(ctx, "missing"))

	// Some S3-compatible stores answer 404 NoSuchKey to a delete.
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		s3XMLError(w, 404, "NoSuchKey", "gone")
		return true
	}
	must(t, s.Delete(ctx, "missing"))
}

func TestS3NoCredentialsInErrors(t *testing.T) {
	f := newFakeS3(t)

	// A wrong secret: the fake refuses the signature.
	wrong := fakeCreds
	wrong.SecretAccessKey = "wrong-secret-LEAKCHECK-0000002"
	s, err := NewS3(S3Config{Endpoint: f.srv.URL, Bucket: fakeBucket}, wrong)
	must(t, err)
	err = s.Check(ctx)
	var se *S3Error
	if !errors.As(err, &se) || se.Code != "SignatureDoesNotMatch" {
		t.Fatalf("got %v, want SignatureDoesNotMatch", err)
	}
	assertNoCredentials(t, err)
	if strings.Contains(err.Error(), wrong.SecretAccessKey) {
		t.Fatalf("the error %q contains the secret", err)
	}

	// No answer at all: nothing listens on the port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	addr := ln.Addr().String()
	ln.Close()
	s, err = NewS3(S3Config{Endpoint: "http://" + addr, Bucket: fakeBucket}, fakeCreds)
	must(t, err)
	s.retryBase = time.Millisecond
	_, err = s.CreateUpload(ctx, "k")
	if err == nil || !strings.Contains(err.Error(), "no answer") || !strings.Contains(err.Error(), "4 attempts") {
		t.Fatalf("got %v, want a no-answer error after 4 attempts", err)
	}
	assertNoCredentials(t, err)
	if strings.Contains(err.Error(), "uploads=") {
		t.Errorf("the error %q contains the request URL", err)
	}
}

func TestS3RetryHonoursContext(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	s.retryBase = 10 * time.Second
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		s3XMLError(w, 503, "SlowDown", "slow down")
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Stat(cctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the context's deadline", err)
	}
	if time.Since(start) > testlib.Slow(5*time.Second) {
		t.Fatalf("waited %v despite the context", time.Since(start))
	}
}

func TestS3SendsSignedHashes(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	var sawMD5, sawSHA atomic.Bool
	f.hook = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.Method == "PUT" && r.URL.Query().Has("partNumber") {
			sawMD5.Store(r.Header.Get("Content-MD5") != "")
			sawSHA.Store(r.Header.Get("X-Amz-Content-Sha256") == hexSHA256(body) && strings.Contains(r.Header.Get("Authorization"), "x-amz-content-sha256"))
		}
		return false
	}
	putObject(t, s, "hashed", []byte("payload"))
	if !sawMD5.Load() || !sawSHA.Load() {
		t.Fatalf("UploadPart: Content-MD5 sent %v, signed payload SHA-256 %v", sawMD5.Load(), sawSHA.Load())
	}

	// A body changed in transit would no longer match the signed hash.
	id, err := s.CreateUpload(ctx, "tampered")
	must(t, err)
	f.hook = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.Method == "PUT" {
			r2 := r.Clone(r.Context())
			if f.checkSignature(r2, append(body, 'X')) == "" {
				t.Error("a changed body passed the checks")
			}
		}
		return false
	}
	_, err = s.UploadPart(ctx, "tampered", id, 1, []byte("abc"))
	must(t, err)
}

func TestS3OpenRanges(t *testing.T) {
	f := newFakeS3(t)
	s := f.store(t, "")
	putObject(t, s, "r", []byte("0123456789"))
	var ranges []string
	var mu sync.Mutex
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		return false
	}
	for _, tc := range []struct {
		off, n int64
		want   string
	}{{0, -1, "0123456789"}, {2, 3, "234"}, {7, -1, "789"}, {8, 100, "89"}, {10, -1, ""}, {50, 2, ""}, {3, 0, ""}} {
		if got := readAll(t, s, "r", tc.off, tc.n); string(got) != tc.want {
			t.Errorf("Open(%d, %d) = %q, want %q", tc.off, tc.n, got, tc.want)
		}
	}
	want := []string{"", "bytes=2-4", "bytes=7-", "bytes=8-107", "bytes=10-", "bytes=50-51", ""}
	if strings.Join(ranges, ",") != strings.Join(want, ",") {
		t.Errorf("Range headers %q, want %q", ranges, want)
	}

	// A store that ignores Range and sends the whole object.
	f.hook = func(w http.ResponseWriter, r *http.Request, _ []byte) bool {
		if r.Method == "GET" {
			io.WriteString(w, "0123456789")
			return true
		}
		return false
	}
	if got := readAll(t, s, "r", 2, 3); string(got) != "234" {
		t.Errorf("with the range ignored: got %q", got)
	}
}

func TestS3RequestURLs(t *testing.T) {
	no := false
	tests := []struct {
		cfg      S3Config
		key      string
		wantHost string
		wantPath string
	}{
		{S3Config{Endpoint: "http://minio:9000", Bucket: "bk1"}, "a/b.txt", "minio:9000", "/bk1/a/b.txt"},
		{S3Config{Endpoint: "https://s3.eu-west-1.amazonaws.com/", Bucket: "bk1", Prefix: "launchway"}, "k", "s3.eu-west-1.amazonaws.com", "/bk1/launchway/k"},
		{S3Config{Endpoint: "https://s3.example.com", Bucket: "bk1", Prefix: "/launchway//"}, "k", "s3.example.com", "/bk1/launchway/k"},
		{S3Config{Endpoint: "https://s3.example.com", Bucket: "bk1", Prefix: "x/y/"}, "k/l", "s3.example.com", "/bk1/x/y/k/l"},
		{S3Config{Endpoint: "https://s3.example.com", Bucket: "my.bucket", PathStyle: &no}, "k", "my.bucket.s3.example.com", "/k"},
		{S3Config{Endpoint: "https://s3.example.com", Bucket: "bk1", PathStyle: &no, Prefix: "p/"}, "k", "bk1.s3.example.com", "/p/k"},
		{S3Config{Endpoint: "https://s3.example.com", Bucket: "bk1"}, "", "s3.example.com", "/bk1"},
	}
	for _, tc := range tests {
		s, err := NewS3(tc.cfg, fakeCreds)
		must(t, err)
		req, err := s.newRequest(ctx, s3Request{op: "test", method: "GET", key: tc.key, query: encodeQuery("uploadId", "a/b+c")})
		must(t, err)
		if req.URL.Host != tc.wantHost || req.URL.EscapedPath() != tc.wantPath {
			t.Errorf("%+v %q: got %s %s, want %s %s", tc.cfg, tc.key, req.URL.Host, req.URL.EscapedPath(), tc.wantHost, tc.wantPath)
		}
		if req.URL.RawQuery != "uploadId=a%2Fb%2Bc" {
			t.Errorf("query %q", req.URL.RawQuery)
		}
		if req.Header.Get("X-Amz-Content-Sha256") != emptySHA256 || !authRE.MatchString(req.Header.Get("Authorization")) {
			t.Errorf("request not signed: %v", req.Header)
		}
	}
}

func TestNewS3Validation(t *testing.T) {
	ok := S3Config{Endpoint: "https://s3.example.com", Bucket: "bucket"}
	bad := []struct {
		name  string
		cfg   func(c *S3Config)
		creds S3Credentials
		want  string
	}{
		{"no endpoint", func(c *S3Config) { c.Endpoint = "" }, fakeCreds, "endpoint"},
		{"ftp", func(c *S3Config) { c.Endpoint = "ftp://s3.example.com" }, fakeCreds, "https://"},
		{"no scheme", func(c *S3Config) { c.Endpoint = "s3.example.com" }, fakeCreds, "https://"},
		{"path", func(c *S3Config) { c.Endpoint = "https://s3.example.com/bucket" }, fakeCreds, "without a path"},
		{"user", func(c *S3Config) { c.Endpoint = "https://user:pw@s3.example.com" }, fakeCreds, "must not contain credentials"},
		{"no bucket", func(c *S3Config) { c.Bucket = "" }, fakeCreds, "needs a bucket"},
		{"bad bucket", func(c *S3Config) { c.Bucket = "My_Bucket" }, fakeCreds, "not a valid S3 bucket name"},
		{"short bucket", func(c *S3Config) { c.Bucket = "ab" }, fakeCreds, "not a valid S3 bucket name"},
		{"bucket dash end", func(c *S3Config) { c.Bucket = "abc-" }, fakeCreds, "not a valid S3 bucket name"},
		{"bad region", func(c *S3Config) { c.Region = "EU West" }, fakeCreds, "region"},
		{"bad prefix", func(c *S3Config) { c.Prefix = "Launchway/" }, fakeCreds, "prefix"},
		{"prefix dots", func(c *S3Config) { c.Prefix = "a/../b" }, fakeCreds, "prefix"},
		{"no key id", func(*S3Config) {}, S3Credentials{SecretAccessKey: "s"}, "access key ID"},
		{"no secret", func(*S3Config) {}, S3Credentials{AccessKeyID: "a"}, "secret access key"},
		{"bad ca", func(c *S3Config) { c.CAFile = "/nonexistent/ca.pem" }, fakeCreds, "CA bundle"},
	}
	for _, tc := range bad {
		cfg := ok
		tc.cfg(&cfg)
		_, err := NewS3(cfg, tc.creds)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error containing %q", tc.name, err, tc.want)
		}
		assertNoCredentials(t, err)
	}
	s, err := NewS3(ok, fakeCreds)
	must(t, err)
	if s.Name() != "s3" || s.signer.region != DefaultS3Region || !s.pathStyle || s.prefix != "" {
		t.Errorf("defaults: name %q region %q path style %v prefix %q", s.Name(), s.signer.region, s.pathStyle, s.prefix)
	}

	// A CA bundle that is not PEM is refused; a PEM one is accepted.
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	must(t, os.WriteFile(junk, []byte("not a certificate"), 0o600))
	cfg := ok
	cfg.CAFile = junk
	if _, err := NewS3(cfg, fakeCreds); err == nil || !strings.Contains(err.Error(), "no PEM certificates") {
		t.Errorf("junk CA bundle: %v", err)
	}
	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()
	pemFile := filepath.Join(dir, "ca.pem")
	must(t, os.WriteFile(pemFile, certPEM(tlsSrv), 0o600))
	cfg.CAFile = pemFile
	if _, err := NewS3(cfg, fakeCreds); err != nil {
		t.Errorf("PEM CA bundle: %v", err)
	}
}

func TestS3CustomCA(t *testing.T) {
	// The fake behind TLS with its own certificate: trusted only through
	// the CA bundle.
	f := newFakeS3(t)
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes are expected
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	pemFile := filepath.Join(t.TempDir(), "ca.pem")
	must(t, os.WriteFile(pemFile, certPEM(tlsSrv), 0o600))

	s, err := NewS3(S3Config{Endpoint: tlsSrv.URL, Bucket: fakeBucket}, fakeCreds)
	must(t, err)
	s.retryBase = time.Millisecond
	if err := s.Check(ctx); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	s, err = NewS3(S3Config{Endpoint: tlsSrv.URL, Bucket: fakeBucket, CAFile: pemFile}, fakeCreds)
	must(t, err)
	must(t, s.Check(ctx))
}

func certPEM(srv *httptest.Server) []byte {
	cert := srv.Certificate()
	var b bytes.Buffer
	b.WriteString("-----BEGIN CERTIFICATE-----\n")
	enc := base64.StdEncoding.EncodeToString(cert.Raw)
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n-----END CERTIFICATE-----\n")
	return b.Bytes()
}

func TestCredentialsFromEnv(t *testing.T) {
	clear := func() {
		for _, v := range []string{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID_FILE", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY_FILE", "AWS_SESSION_TOKEN"} {
			t.Setenv(v, "")
		}
	}
	clear()
	_, err := CredentialsFromEnv()
	if err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") {
		t.Fatalf("nothing set: %v", err)
	}

	t.Setenv("AWS_ACCESS_KEY_ID", "AKID-visible")
	_, err = CredentialsFromEnv()
	if err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") || strings.Contains(err.Error(), "AKID-visible") {
		t.Fatalf("no secret: %v", err)
	}

	t.Setenv("AWS_SECRET_ACCESS_KEY", " secret-value-1 \n")
	t.Setenv("AWS_SESSION_TOKEN", "tok")
	c, err := CredentialsFromEnv()
	must(t, err)
	if c != (S3Credentials{AccessKeyID: "AKID-visible", SecretAccessKey: "secret-value-1", SessionToken: "tok"}) {
		t.Fatalf("got %+v", c)
	}

	// Files, as Kubernetes mounts secrets.
	clear()
	dir := t.TempDir()
	idFile, secretFile := filepath.Join(dir, "id"), filepath.Join(dir, "secret")
	must(t, os.WriteFile(idFile, []byte("AKID-from-file\n"), 0o600))
	must(t, os.WriteFile(secretFile, []byte("secret-from-file\n"), 0o600))
	t.Setenv("AWS_ACCESS_KEY_ID_FILE", idFile)
	t.Setenv("AWS_SECRET_ACCESS_KEY_FILE", secretFile)
	c, err = CredentialsFromEnv()
	must(t, err)
	if c.AccessKeyID != "AKID-from-file" || c.SecretAccessKey != "secret-from-file" {
		t.Fatalf("got %+v", c)
	}

	// Both a value and a file: refused rather than guessing.
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-value-2")
	_, err = CredentialsFromEnv()
	if err == nil || !strings.Contains(err.Error(), "only one") || strings.Contains(err.Error(), "secret-value-2") || strings.Contains(err.Error(), "secret-from-file") {
		t.Fatalf("both set: %v", err)
	}
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	// A missing or empty file.
	t.Setenv("AWS_SECRET_ACCESS_KEY_FILE", filepath.Join(dir, "missing"))
	if _, err = CredentialsFromEnv(); err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY_FILE") {
		t.Fatalf("missing file: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	must(t, os.WriteFile(empty, []byte("\n"), 0o600))
	t.Setenv("AWS_SECRET_ACCESS_KEY_FILE", empty)
	if _, err = CredentialsFromEnv(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestS3ErrorMessages(t *testing.T) {
	e := &S3Error{Op: "upload part 3", Key: "p/k", StatusCode: 400, Code: "EntityTooSmall", Message: "too small", RequestID: "R1"}
	if got := e.Error(); got != "s3 upload part 3 p/k: EntityTooSmall: too small (HTTP 400, request ID R1)" {
		t.Errorf("got %q", got)
	}
	for code, want := range map[string]bool{"NoSuchKey": true, "NoSuchUpload": true, "NotFound": true, "NoSuchBucket": false, "AccessDenied": false} {
		if got := errors.Is(&S3Error{Code: code}, ErrNotFound); got != want {
			t.Errorf("errors.Is(%s, ErrNotFound) = %v", code, got)
		}
	}
	if !isErrorDocument([]byte("<?xml version=\"1.0\"?>\n<Error><Code>X</Code></Error>")) || isErrorDocument([]byte("<CompleteMultipartUploadResult/>")) || isErrorDocument(nil) {
		t.Error("isErrorDocument is wrong")
	}
}
