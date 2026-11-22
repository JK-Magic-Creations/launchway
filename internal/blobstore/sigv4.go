package blobstore

import (
	"net/http"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
)

// Request signing (AWS Signature Version 4) lives in internal/awssig, which the
// AWS CodeCommit provider shares; this file keeps the names the S3 client and
// its tests use.

const (
	amzDateFormat = awssig.DateFormat
	// emptySHA256 is the hex SHA-256 of no bytes.
	emptySHA256 = awssig.EmptySHA256
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
// to req and then its Authorization header; see awssig.Signer.Sign.
func (s signer) sign(req *http.Request, payloadHash string, now time.Time) signature {
	g := awssig.Signer{Creds: awssig.Credentials{AccessKeyID: s.creds.AccessKeyID, SecretAccessKey: s.creds.SecretAccessKey,
		SessionToken: s.creds.SessionToken}, Region: s.region, Service: s.service}.Sign(req, payloadHash, now)
	return signature{canonicalRequest: g.CanonicalRequest, stringToSign: g.StringToSign, signedHeaders: g.SignedHeaders, signature: g.Signature}
}

func canonicalQuery(raw string) string            { return awssig.CanonicalQuery(raw) }
func uriEncode(s string, encodeSlash bool) string { return awssig.URIEncode(s, encodeSlash) }
func encodeQuery(pairs ...string) string          { return awssig.EncodeQuery(pairs...) }
func hexSHA256(b []byte) string                   { return awssig.HexSHA256(b) }
