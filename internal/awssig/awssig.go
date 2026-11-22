// Package awssig signs requests with AWS Signature Version 4, as described in
// "Create a signed AWS API request" in the IAM user guide and in "Signature
// Calculations for the Authorization Header" in the S3 API reference. It is
// the one implementation the object store client (internal/blobstore) and the
// AWS CodeCommit provider share; it uses the standard library only (no AWS SDK).
package awssig

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

const (
	// Algorithm is the signing algorithm named in every Authorization header and string to sign.
	Algorithm = "AWS4-HMAC-SHA256"
	// DateFormat is the layout of X-Amz-Date.
	DateFormat = "20060102T150405Z"
	// EmptySHA256 is the hex SHA-256 of no bytes.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Credentials are an access key pair and, for temporary credentials, the session token.
type Credentials struct{ AccessKeyID, SecretAccessKey, SessionToken string }

// Signer signs requests for one service in one region.
type Signer struct {
	Creds   Credentials
	Region  string
	Service string // "s3", "codecommit", "sts"
}

// Signature is the pieces of one signing, kept for tests.
type Signature struct {
	CanonicalRequest string
	StringToSign     string
	SignedHeaders    string
	Signature        string
}

// Sign adds X-Amz-Date (and X-Amz-Security-Token for temporary credentials)
// to req and then its Authorization header. payloadHash is the hex SHA-256
// of the body; for S3 the caller also sends it as X-Amz-Content-Sha256.
// Every header already in req.Header is signed, along with Host; headers the
// transport adds later (User-Agent, Content-Length, Accept-Encoding) are not.
func (s Signer) Sign(req *http.Request, payloadHash string, now time.Time) Signature {
	now = now.UTC()
	amzDate := now.Format(DateFormat)
	req.Header.Set("X-Amz-Date", amzDate)
	if s.Creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.Creds.SessionToken)
	}
	req.Header.Del("Authorization")

	headers, signed := canonicalHeaders(req)
	canonical := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		CanonicalQuery(req.URL.RawQuery),
		headers,
		signed,
		payloadHash,
	}, "\n")

	day := now.Format("20060102")
	scope := day + "/" + s.Region + "/" + s.Service + "/aws4_request"
	toSign := Algorithm + "\n" + amzDate + "\n" + scope + "\n" + HexSHA256([]byte(canonical))

	sig := Signing(s.Creds.SecretAccessKey, day, s.Region, s.Service, toSign)

	req.Header.Set("Authorization", Algorithm+" Credential="+s.Creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
	return Signature{CanonicalRequest: canonical, StringToSign: toSign, SignedHeaders: signed, Signature: sig}
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

// CanonicalQuery sorts the query parameters by name, then value, and
// encodes both per SigV4 (a parameter without a value gets "=").
func CanonicalQuery(raw string) string {
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
		params = append(params, kv{URIEncode(k, true), URIEncode(v, true)})
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

// URIEncode encodes s per SigV4: the unreserved characters A-Z a-z 0-9
// '-' '.' '_' '~' are kept, everything else becomes %XX in upper-case hex.
// '/' is kept unless encodeSlash is set.
func URIEncode(s string, encodeSlash bool) string {
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

// EncodeQuery builds a query string from name/value pairs with the SigV4
// encoding, so what is sent is exactly what is signed.
func EncodeQuery(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, URIEncode(pairs[i], true)+"="+URIEncode(pairs[i+1], true))
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func HexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Signing derives the signing key for the day (YYYYMMDD), region and service
// from the secret access key and signs the string to sign with it: the last
// step of Signature Version 4, in hex.
func Signing(secret, day, region, service, stringToSign string) string {
	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, stringToSign))
}

// Scope is the credential scope: day/region/service/aws4_request.
func Scope(day, region, service string) string {
	return day + "/" + region + "/" + service + "/aws4_request"
}
