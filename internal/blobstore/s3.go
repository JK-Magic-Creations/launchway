package blobstore

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// S3Config says where an S3-compatible object store is. Credentials are
// never in the configuration file: see CredentialsFromEnv.
type S3Config struct {
	Endpoint  string `yaml:"endpoint"`   // e.g. https://s3.eu-west-1.amazonaws.com or http://minio:9000; required
	Region    string `yaml:"region"`     // default us-east-1
	Bucket    string `yaml:"bucket"`     // required; must exist
	Prefix    string `yaml:"prefix"`     // prepended to every key, e.g. "launchway/"; optional
	PathStyle *bool  `yaml:"path_style"` // default true (works with MinIO, Ceph and AWS); false = virtual-hosted style
	// Credentials are never in the configuration file: AccessKeyID, SecretAccessKey and SessionToken
	// come from the environment variables AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN
	// (or files named by AWS_ACCESS_KEY_ID_FILE / AWS_SECRET_ACCESS_KEY_FILE, read at start, for Kubernetes secrets mounted as files).
	CAFile string `yaml:"ca_file"` // optional extra CA bundle (PEM) for a private endpoint
}

// S3Credentials sign requests to the object store. SessionToken is only set
// for temporary credentials.
type S3Credentials struct{ AccessKeyID, SecretAccessKey, SessionToken string }

// DefaultS3Region is the region used when the configuration names none.
const DefaultS3Region = "us-east-1"

const (
	// maxXMLAnswer bounds the XML answers the client reads.
	maxXMLAnswer = 1 << 20
	// s3Attempts is how many times a request is tried in all.
	s3Attempts = 4
	// s3RetryBase is the first wait between attempts; it doubles each time.
	s3RetryBase = 200 * time.Millisecond
	s3RetryMax  = 5 * time.Second
)

// CredentialsFromEnv reads the S3 credentials from AWS_ACCESS_KEY_ID (or the
// file named by AWS_ACCESS_KEY_ID_FILE), AWS_SECRET_ACCESS_KEY (or
// AWS_SECRET_ACCESS_KEY_FILE) and, optionally, AWS_SESSION_TOKEN. Errors
// name the variable, never its value.
func CredentialsFromEnv() (S3Credentials, error) {
	id, err := envOrFile("AWS_ACCESS_KEY_ID")
	if err != nil {
		return S3Credentials{}, err
	}
	secret, err := envOrFile("AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return S3Credentials{}, err
	}
	return S3Credentials{
		AccessKeyID:     id,
		SecretAccessKey: secret,
		SessionToken:    strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN")),
	}, nil
}

// envOrFile reads a required credential from the variable name, or from
// the file named by name_FILE.
func envOrFile(name string) (string, error) {
	val, file := strings.TrimSpace(os.Getenv(name)), os.Getenv(name+"_FILE")
	switch {
	case val != "" && file != "":
		return "", fmt.Errorf("both %s and %s_FILE are set; set only one", name, name)
	case val != "":
		return val, nil
	case file == "":
		return "", fmt.Errorf("the S3 artifact store needs credentials: %s (or %s_FILE, naming a file that holds it) is not set", name, name)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading %s_FILE: %w", name, err)
	}
	if val = strings.TrimSpace(string(b)); val == "" {
		return "", fmt.Errorf("the file named by %s_FILE (%s) is empty", name, file)
	}
	return val, nil
}

// S3 stores objects in a bucket of an S3-compatible object store (AWS S3,
// MinIO, Ceph RGW and others), over HTTP with Signature Version 4. Multipart
// upload state lives in the object store, so any server can take any part.
//
// Requests are retried, with exponential backoff and jitter, on network
// errors, on 408, 429, 500, 502, 503 and 504 answers and on SlowDown, at most
// four attempts in all. Every request this store makes is idempotent for its
// use: a retried CreateUpload may leave an unused upload behind, which a
// bucket lifecycle rule (AbortIncompleteMultipartUpload) cleans up.
type S3 struct {
	scheme    string
	host      string // the endpoint's host[:port]
	bucket    string
	prefix    string // "" or ending in exactly one '/'
	pathStyle bool
	signer    signer
	client    *http.Client

	now       func() time.Time
	retryBase time.Duration
}

// NewS3 checks the configuration and returns a store. It does not contact
// the endpoint; Check does.
func NewS3(cfg S3Config, creds S3Credentials) (*S3, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("the S3 artifact store needs an endpoint, for example https://s3.eu-west-1.amazonaws.com")
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("the S3 endpoint %q is not a URL", cfg.Endpoint)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("the S3 endpoint %q must start with https:// or http://", cfg.Endpoint)
	case u.Host == "" || u.Hostname() == "":
		return nil, fmt.Errorf("the S3 endpoint %q has no host", cfg.Endpoint)
	case u.User != nil:
		return nil, errors.New("the S3 endpoint must not contain credentials; set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY instead")
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "":
		return nil, fmt.Errorf("the S3 endpoint %q must be only a scheme and a host, without a path or query", cfg.Endpoint)
	}
	if err := checkBucketName(cfg.Bucket); err != nil {
		return nil, err
	}
	region := cfg.Region
	if region == "" {
		region = DefaultS3Region
	}
	for i := 0; i < len(region); i++ {
		if c := region[i]; !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-') {
			return nil, fmt.Errorf("the S3 region %q may only have lower-case letters, digits and '-'", region)
		}
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		if err := ValidateKey(prefix); err != nil {
			return nil, fmt.Errorf("the S3 prefix %q is not usable: %w", cfg.Prefix, err)
		}
		prefix += "/"
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, errors.New("the S3 artifact store needs an access key ID and a secret access key (AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)")
	}
	pathStyle := true
	if cfg.PathStyle != nil {
		pathStyle = *cfg.PathStyle
	}
	client, err := newS3HTTPClient(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	return &S3{
		scheme:    u.Scheme,
		host:      u.Host,
		bucket:    cfg.Bucket,
		prefix:    prefix,
		pathStyle: pathStyle,
		signer:    signer{creds: creds, region: region, service: "s3"},
		client:    client,
		now:       time.Now,
		retryBase: s3RetryBase,
	}, nil
}

// checkBucketName applies S3's bucket naming rules: 3 to 63 lower-case
// letters, digits, '.' and '-', starting and ending with a letter or digit.
func checkBucketName(b string) error {
	if b == "" {
		return errors.New("the S3 artifact store needs a bucket")
	}
	ok := len(b) >= 3 && len(b) <= 63
	for i := 0; ok && i < len(b); i++ {
		c := b[i]
		alnum := 'a' <= c && c <= 'z' || '0' <= c && c <= '9'
		ok = alnum || (c == '.' || c == '-') && i > 0 && i < len(b)-1
	}
	if !ok || strings.Contains(b, "..") {
		return fmt.Errorf("%q is not a valid S3 bucket name (3 to 63 lower-case letters, digits, '.' and '-', starting and ending with a letter or digit)", b)
	}
	return nil
}

// newS3HTTPClient returns a client with bounded connect, TLS and
// response-header times. There is no overall timeout, so large objects can
// stream; callers bound each request with their context.
func newS3HTTPClient(caFile string) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading the S3 CA bundle: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("the S3 CA bundle %s has no PEM certificates", caFile)
		}
		tlsConfig.RootCAs = pool
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute, // completing a large upload can take a while
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		// S3 answers never redirect for path-style requests; following a
		// redirect would resend a request signed for another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// Name returns "s3".
func (s *S3) Name() string { return "s3" }

// Check writes, reads back and deletes a small probe object.
func (s *S3) Check(ctx context.Context) error {
	return checkStore(ctx, s, func(key string, data []byte) error {
		return s.putObject(ctx, key, data)
	})
}

// putObject writes a whole object in one request.
func (s *S3) putObject(ctx context.Context, key string, data []byte) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-MD5", contentMD5(data))
	_, _, err := s.do(ctx, s3Request{op: "put object", method: http.MethodPut, key: key, body: data, header: h})
	return err
}

// CreateUpload starts a multipart upload (CreateMultipartUpload).
func (s *S3) CreateUpload(ctx context.Context, key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	_, body, err := s.do(ctx, s3Request{op: "create upload", method: http.MethodPost, key: key, query: encodeQuery("uploads", ""), header: h})
	if err != nil {
		return "", err
	}
	var res struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(body, &res); err != nil || res.UploadID == "" {
		return "", fmt.Errorf("s3 create upload %s: the answer has no upload ID", s.prefix+key)
	}
	return res.UploadID, nil
}

// UploadPart stores part n (UploadPart). The body is protected in transit
// by its signed SHA-256 and its Content-MD5. The ETag is returned without
// quotes.
func (s *S3) UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (string, error) {
	if err := s.checkUploadArgs(key, uploadID); err != nil {
		return "", err
	}
	if err := checkPartNumber(n); err != nil {
		return "", err
	}
	h := http.Header{}
	h.Set("Content-MD5", contentMD5(data))
	resp, _, err := s.do(ctx, s3Request{
		op: "upload part " + strconv.Itoa(n), method: http.MethodPut, key: key,
		query: encodeQuery("partNumber", strconv.Itoa(n), "uploadId", uploadID), body: data, header: h,
	})
	if err != nil {
		return "", err
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		return "", fmt.Errorf("s3 upload part %d of %s: the answer has no ETag", n, s.prefix+key)
	}
	return etag, nil
}

// CompleteUpload joins the parts into the object (CompleteMultipartUpload).
// If S3 no longer knows the upload but the object exists, an earlier
// attempt completed it, and this one succeeds too.
func (s *S3) CompleteUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	if err := s.checkUploadArgs(key, uploadID); err != nil {
		return err
	}
	if err := checkParts(parts); err != nil {
		return err
	}
	type xmlPart struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	doc := struct {
		XMLName xml.Name  `xml:"http://s3.amazonaws.com/doc/2006-03-01/ CompleteMultipartUpload"`
		Parts   []xmlPart `xml:"Part"`
	}{}
	for _, p := range parts {
		doc.Parts = append(doc.Parts, xmlPart{PartNumber: p.Number, ETag: `"` + strings.Trim(p.ETag, `"`) + `"`})
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("s3 complete upload %s: %w", s.prefix+key, err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/xml")
	_, _, err = s.do(ctx, s3Request{
		op: "complete upload", method: http.MethodPost, key: key,
		query: encodeQuery("uploadId", uploadID), body: body, header: h, errorIn200: true,
	})
	if errors.Is(err, ErrNotFound) {
		if _, serr := s.Stat(ctx, key); serr == nil {
			return nil // completed by an earlier attempt
		}
	}
	return err
}

// AbortUpload removes an upload and its parts (AbortMultipartUpload). An
// unknown upload is not an error.
func (s *S3) AbortUpload(ctx context.Context, key, uploadID string) error {
	if err := s.checkUploadArgs(key, uploadID); err != nil {
		return err
	}
	_, _, err := s.do(ctx, s3Request{op: "abort upload", method: http.MethodDelete, key: key, query: encodeQuery("uploadId", uploadID)})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Open reads length bytes of an object from offset (length < 0: to the
// end) with a ranged GetObject. The body streams from the store, so ctx
// must stay alive until the reader is closed.
func (s *S3) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	if err := checkRange(offset); err != nil {
		return nil, err
	}
	if length == 0 {
		if _, err := s.Stat(ctx, key); err != nil {
			return nil, err
		}
		return emptyReader{}, nil
	}
	h := http.Header{}
	if offset > 0 || length > 0 {
		r := "bytes=" + strconv.FormatInt(offset, 10) + "-"
		if length > 0 {
			r += strconv.FormatInt(offset+length-1, 10)
		}
		h.Set("Range", r)
	}
	resp, _, err := s.do(ctx, s3Request{op: "get object", method: http.MethodGet, key: key, header: h, stream: true})
	var se *S3Error
	if errors.As(err, &se) && se.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return emptyReader{}, nil // the offset is at or past the end
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK && h.Get("Range") != "" {
		// The store ignored the range and sent the whole object: skip to
		// the offset and stop after length bytes.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil && err != io.EOF {
			resp.Body.Close()
			return nil, fmt.Errorf("s3 get object %s: %v", s.prefix+key, err)
		}
		if length > 0 {
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(resp.Body, length), resp.Body}, nil
		}
	}
	return resp.Body, nil
}

// Stat returns the size of an object (HeadObject).
func (s *S3) Stat(ctx context.Context, key string) (int64, error) {
	if err := ValidateKey(key); err != nil {
		return 0, err
	}
	resp, _, err := s.do(ctx, s3Request{op: "stat object", method: http.MethodHead, key: key})
	if err != nil {
		return 0, err
	}
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("s3 stat object %s: the answer has no Content-Length", s.prefix+key)
	}
	return resp.ContentLength, nil
}

// Delete removes an object (DeleteObject). A missing object is not an
// error.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	_, _, err := s.do(ctx, s3Request{op: "delete object", method: http.MethodDelete, key: key})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// checkUploadArgs checks a key and an upload ID given back by a caller.
// S3 upload IDs are opaque, so only their length and characters are
// checked.
func (s *S3) checkUploadArgs(key, uploadID string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if uploadID == "" || len(uploadID) > 1024 {
		return fmt.Errorf("%q is not an S3 upload ID", uploadID)
	}
	for i := 0; i < len(uploadID); i++ {
		if c := uploadID[i]; c <= ' ' || c >= 0x7f {
			return fmt.Errorf("%q is not an S3 upload ID", uploadID)
		}
	}
	return nil
}

// s3Request is one S3 operation, retried as a whole.
type s3Request struct {
	op     string // for errors, e.g. "upload part 3"
	method string
	key    string // without the prefix; "" for the bucket itself
	query  string // built with encodeQuery
	body   []byte
	header http.Header
	// errorIn200 marks CompleteMultipartUpload, which can fail after
	// answering 200 OK, with an <Error> document as the body.
	errorIn200 bool
	// stream returns a successful answer with its body unread (GetObject).
	stream bool
}

// do sends r, retrying when that may help, and returns the successful
// answer. The body of a successful answer is read (up to maxXMLAnswer) and
// closed, unless r.stream is set. A failure is an *S3Error when the store
// answered, and another error when it did not.
func (s *S3) do(ctx context.Context, r s3Request) (*http.Response, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= s3Attempts; attempt++ {
		if attempt > 1 {
			if err := s.wait(ctx, attempt-1); err != nil {
				return nil, nil, fmt.Errorf("%w (after: %v)", err, lastErr)
			}
		}
		req, err := s.newRequest(ctx, r)
		if err != nil {
			return nil, nil, err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, fmt.Errorf("s3 %s %s: %w", r.op, s.prefix+r.key, ctx.Err())
			}
			lastErr = s.networkError(r, err)
			continue
		}
		if resp.StatusCode/100 == 2 {
			if r.stream {
				return resp, nil, nil
			}
			body, err := readLimited(resp.Body)
			resp.Body.Close()
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, fmt.Errorf("s3 %s %s: %w", r.op, s.prefix+r.key, ctx.Err())
				}
				lastErr = fmt.Errorf("s3 %s %s: reading the answer: %v", r.op, s.prefix+r.key, s.redact(err.Error()))
				continue
			}
			if r.errorIn200 && isErrorDocument(body) {
				e := s.newError(r, resp, body)
				if retryableCode(e.Code) {
					lastErr = e
					continue
				}
				return nil, nil, e
			}
			return resp, body, nil
		}
		body, _ := readLimited(resp.Body)
		resp.Body.Close()
		e := s.newError(r, resp, body)
		if retryableStatus(resp.StatusCode) || retryableCode(e.Code) && resp.StatusCode >= 500 {
			lastErr = e
			continue
		}
		return nil, nil, e
	}
	return nil, nil, fmt.Errorf("%w (gave up after %d attempts)", lastErr, s3Attempts)
}

// newRequest builds and signs one attempt of r.
func (s *S3) newRequest(ctx context.Context, r s3Request) (*http.Request, error) {
	host, escaped := s.host, ""
	full := ""
	if r.key != "" {
		full = s.prefix + r.key
	}
	if s.pathStyle {
		escaped = "/" + uriEncode(s.bucket, false)
		if full != "" {
			escaped += "/" + uriEncode(full, false)
		}
	} else {
		host = s.bucket + "." + s.host
		escaped = "/" + uriEncode(full, false)
	}
	unescaped, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, fmt.Errorf("s3 %s: building the request path: %w", r.op, err)
	}
	u := &url.URL{Scheme: s.scheme, Host: host, Path: unescaped, RawPath: escaped, RawQuery: r.query}
	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), bytes.NewReader(r.body))
	if err != nil {
		return nil, fmt.Errorf("s3 %s %s: building the request: %w", r.op, full, err)
	}
	for name, vs := range r.header {
		req.Header[name] = append([]string(nil), vs...)
	}
	payloadHash := emptySHA256
	if len(r.body) > 0 {
		payloadHash = hexSHA256(r.body)
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	s.signer.sign(req, payloadHash, s.now())
	req.Header.Set("User-Agent", "launchway")
	return req, nil
}

// wait sleeps before retry number n (1, 2, ...): the base doubled n-1
// times, capped, with jitter so that many clients do not retry in step.
func (s *S3) wait(ctx context.Context, n int) error {
	d := s.retryBase << (n - 1)
	if d > s3RetryMax || d <= 0 {
		d = s3RetryMax
	}
	d = d/2 + rand.N(d/2+1)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// networkError describes a request that got no answer. The URL is left
// out: it carries the upload ID and is long, and the endpoint is named
// instead.
func (s *S3) networkError(r s3Request, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("s3 %s %s: no answer from %s: %s", r.op, s.prefix+r.key, s.host, s.redact(err.Error()))
}

// redact removes the credentials from a message, should a store or a
// proxy ever echo them back.
func (s *S3) redact(msg string) string {
	for _, secret := range []string{s.signer.creds.SecretAccessKey, s.signer.creds.SessionToken, s.signer.creds.AccessKeyID} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	return msg
}

// S3Error is an error answer from the object store. Code is S3's error
// code, such as AccessDenied, NoSuchBucket, SignatureDoesNotMatch,
// InvalidPart or EntityTooSmall. NoSuchKey, NoSuchUpload and a 404 to a
// HEAD request match ErrNotFound with errors.Is.
type S3Error struct {
	Op         string // what was being done, e.g. "upload part 3"
	Key        string // the object key, prefix included
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *S3Error) Error() string {
	var b strings.Builder
	b.WriteString("s3 " + e.Op)
	if e.Key != "" {
		b.WriteString(" " + e.Key)
	}
	b.WriteString(": " + e.Code)
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	b.WriteString(" (HTTP " + strconv.Itoa(e.StatusCode))
	if e.RequestID != "" {
		b.WriteString(", request ID " + e.RequestID)
	}
	b.WriteString(")")
	return b.String()
}

// Is makes errors.Is(err, ErrNotFound) true for a missing object or upload.
func (e *S3Error) Is(target error) bool {
	return target == ErrNotFound && (e.Code == "NoSuchKey" || e.Code == "NoSuchUpload" || e.Code == "NotFound")
}

// newError reads an S3 error document. Only the code, the message and the
// request ID are kept; other fields (SignatureDoesNotMatch answers include
// the string to sign and the access key ID) are dropped.
func (s *S3) newError(r s3Request, resp *http.Response, body []byte) *S3Error {
	var doc struct {
		Code      string `xml:"Code"`
		Message   string `xml:"Message"`
		RequestID string `xml:"RequestId"`
	}
	_ = xml.Unmarshal(body, &doc)
	e := &S3Error{
		Op:         r.op,
		Key:        s.prefix + r.key,
		StatusCode: resp.StatusCode,
		Code:       s.redact(oneLine(doc.Code, 64)),
		Message:    s.redact(oneLine(doc.Message, 300)),
		RequestID:  oneLine(doc.RequestID, 100),
	}
	if e.RequestID == "" {
		e.RequestID = oneLine(resp.Header.Get("X-Amz-Request-Id"), 100)
	}
	if r.key == "" {
		e.Key = ""
	}
	if e.Code == "" {
		// HEAD answers have no body; name the status instead.
		if resp.StatusCode == http.StatusNotFound && r.method == http.MethodHead {
			e.Code = "NotFound"
		} else {
			e.Code = strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "")
			if e.Code == "" {
				e.Code = "HTTP" + strconv.Itoa(resp.StatusCode)
			}
		}
	}
	return e
}

// oneLine shortens s to at most n bytes on one line.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func retryableCode(code string) bool {
	switch code {
	case "SlowDown", "InternalError", "ServiceUnavailable":
		return true
	}
	return false
}

// isErrorDocument reports whether an XML body is an <Error> document.
func isErrorDocument(body []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local == "Error"
		}
	}
}

// readLimited reads an XML answer of at most maxXMLAnswer bytes.
func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxXMLAnswer+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxXMLAnswer {
		return nil, fmt.Errorf("the answer is longer than %d bytes", maxXMLAnswer)
	}
	return b, nil
}

func contentMD5(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

var _ Store = (*S3)(nil)
