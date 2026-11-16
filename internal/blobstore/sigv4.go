package blobstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// This file signs requests with AWS Signature Version 4, as described in
// "Create a signed AWS API request" in the IAM user guide and in "Signature
// Calculations for the Authorization Header" in the S3 API reference.

const (
	sigAlgorithm  = "AWS4-HMAC-SHA256"
	amzDateFormat = "20060102T150405Z"
	// emptySHA256 is the hex SHA-256 of no bytes.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// signer signs requests for one service in one region.
type signer struct {
	creds   S3Credentials
	region  string
	service string // "s3"
}

// signature is the pieces of one signing, kept for tests.
type signature struct {
	canonicalRequest string
	stringToSign     string
	signedHeaders    string
	signature        string
}

// sign adds X-Amz-Date (and X-Amz-Security-Token for temporary credentials)
// to req and then its Authorization header. payloadHash is the hex SHA-256
// of the body; for S3 the caller also sends it as X-Amz-Content-Sha256.
// Every header already in req.Header is signed, along with Host; headers the
// transport adds later (User-Agent, Content-Length, Accept-Encoding) are not.
func (s signer) sign(req *http.Request, payloadHash string, now time.Time) signature {
	now = now.UTC()
	amzDate := now.Format(amzDateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	if s.creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.creds.SessionToken)
	}
	req.Header.Del("Authorization")

	headers, signed := canonicalHeaders(req)
	canonical := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQuery(req.URL.RawQuery),
		headers,
		signed,
		payloadHash,
	}, "\n")

	day := now.Format("20060102")
	scope := day + "/" + s.region + "/" + s.service + "/aws4_request"
	toSign := sigAlgorithm + "\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canonical))

	key := hmacSHA256([]byte("AWS4"+s.creds.SecretAccessKey), day)
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, s.service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))

	req.Header.Set("Authorization", sigAlgorithm+" Credential="+s.creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
	return signature{canonicalRequest: canonical, stringToSign: toSign, signedHeaders: signed, signature: sig}
}

// canonicalHeaders returns the canonical header block (each "name:value\n",
// sorted by lower-case name) and the signed header list.
func canonicalHeaders(req *http.Request) (string, string) {
	vals := map[string][]string{}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	vals["host"] = []string{host}
	for name, vs := range req.Header {
		lname := strings.ToLower(name)
		switch lname {
		case "authorization", "user-agent", "content-length", "accept-encoding", "host":
			continue
		}
		for _, v := range vs {
			vals[lname] = append(vals[lname], trimHeaderValue(v))
		}
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(strings.Join(vals[n], ","))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// trimHeaderValue trims a header value and collapses runs of spaces, as
// SigV4 requires.
func trimHeaderValue(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// canonicalURI is the request path as sent, which the S3 client builds
// already URI-encoded once (S3 does not encode it a second time).
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQuery sorts the query parameters by name, then value, and
// encodes both per SigV4 (a parameter without a value gets "=").
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var params []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		params = append(params, kv{uriEncode(k, true), uriEncode(v, true)})
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].k != params[j].k {
			return params[i].k < params[j].k
		}
		return params[i].v < params[j].v
	})
	out := make([]string, len(params))
	for i, p := range params {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

// uriEncode encodes s per SigV4: the unreserved characters A-Z a-z 0-9
// '-' '.' '_' '~' are kept, everything else becomes %XX in upper-case hex.
// '/' is kept unless encodeSlash is set.
func uriEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

// encodeQuery builds a query string from name/value pairs with the SigV4
// encoding, so what is sent is exactly what is signed.
func encodeQuery(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, uriEncode(pairs[i], true)+"="+uriEncode(pairs[i+1], true))
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
