package codecommit

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/restkit"
)

// credDoc is what the credential reference holds: one JSON document, so one
// reference carries an access key pair, an optional session token and an
// optional role. Unknown fields are refused (a mistyped name must not be
// silently ignored).
//
//	{"access_key_id": "...", "secret_access_key": "..."}
//	{"access_key_id": "...", "secret_access_key": "...", "role_arn": "arn:aws:iam::123456789012:role/launchway"}
type credDoc struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	// SessionToken is set when the key pair is itself temporary.
	SessionToken string `json:"session_token,omitempty"`
	// RoleARN, when set, is assumed with the key pair (STS AssumeRole) and the
	// temporary credentials it returns are used for every call.
	RoleARN     string `json:"role_arn,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
	SessionName string `json:"role_session_name,omitempty"`
	// DurationSeconds of the role session: 900 to 43200, 3600 when 0.
	DurationSeconds int `json:"duration_seconds,omitempty"`
}

var (
	accessKeyRE   = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
	roleARNRE     = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[\w+=,.@/-]+$`)
	sessionNameRE = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)
	externalIDRE  = regexp.MustCompile(`^[\w+=,.@:/-]+$`)
)

// parseCreds reads and checks the credential document. The error names the
// problem, never a value.
func parseCreds(raw string) (credDoc, error) {
	var d credDoc
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, errors.New("the credential is not the JSON document this host kind reads: {\"access_key_id\": ..., \"secret_access_key\": ...} with optional session_token, role_arn, external_id, role_session_name, duration_seconds")
	}
	switch {
	case !accessKeyRE.MatchString(d.AccessKeyID):
		return d, errors.New("the credential's access_key_id is missing or is not an access key id")
	case d.SecretAccessKey == "":
		return d, errors.New("the credential's secret_access_key is missing")
	case d.RoleARN != "" && !roleARNRE.MatchString(d.RoleARN):
		return d, errors.New("the credential's role_arn is not the ARN of an IAM role")
	case d.SessionName != "" && !sessionNameRE.MatchString(d.SessionName):
		return d, errors.New("the credential's role_session_name must be 2 to 64 letters, digits and _+=,.@-")
	case d.ExternalID != "" && (len(d.ExternalID) < 2 || len(d.ExternalID) > 1224 || !externalIDRE.MatchString(d.ExternalID)):
		return d, errors.New("the credential's external_id has characters the host does not accept")
	case d.DurationSeconds != 0 && (d.DurationSeconds < 900 || d.DurationSeconds > 43200):
		return d, errors.New("the credential's duration_seconds must be between 900 and 43200")
	case d.RoleARN == "" && (d.ExternalID != "" || d.SessionName != "" || d.DurationSeconds != 0):
		return d, errors.New("external_id, role_session_name and duration_seconds go with a role_arn")
	}
	return d, nil
}

// session is what calls are signed with, and who it is.
type session struct {
	creds awssig.Credentials
	// who names the principal for people: the assumed role session's ARN, or the key id masked.
	who     string
	expires time.Time // zero for a static key pair
}

// refreshBefore is how long before expiry temporary credentials are renewed.
const refreshBefore = 5 * time.Minute

// The assumed-role sessions of every connection, in memory only and sealed
// (AES-256-GCM under a key made at the process's start and never written
// anywhere), until shortly before they expire. Providers are made for each
// call, so the cache is shared; its key is an HMAC under the same process key
// of what the session was made from, never a secret in the clear, and what is
// held is the sealed credentials with the (not secret) expiry and name.
var (
	cacheMu  sync.Mutex
	cacheMap = map[string]sealedSession{}

	sealOnce sync.Once
	sealAEAD cipher.AEAD
	sealMAC  []byte
)

const cacheLimit = 256

// maxSessionLife bounds how long a role session is trusted, whatever expiry the
// token service names: the longest a role session can last (43,200 seconds).
const maxSessionLife = 12 * time.Hour

type sealedSession struct {
	box     []byte // nonce, then the sealed credentials
	who     string
	expires time.Time
}

// processKey makes the key sealed sessions and cache keys are made with.
func processKey() (cipher.AEAD, []byte) {
	sealOnce.Do(func() {
		k := make([]byte, 64)
		if _, err := rand.Read(k); err != nil {
			panic("no randomness for the session cache: " + err.Error())
		}
		b, _ := aes.NewCipher(k[:32])
		sealAEAD, _ = cipher.NewGCM(b)
		sealMAC = k[32:]
	})
	return sealAEAD, sealMAC
}

func seal(s session) sealedSession {
	aead, _ := processKey()
	plain := []byte(s.creds.AccessKeyID + "\x00" + s.creds.SecretAccessKey + "\x00" + s.creds.SessionToken)
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	return sealedSession{box: aead.Seal(nonce, nonce, plain, nil), who: s.who, expires: s.expires}
}

func (c sealedSession) open() (session, bool) {
	aead, _ := processKey()
	if len(c.box) < aead.NonceSize() {
		return session{}, false
	}
	plain, err := aead.Open(nil, c.box[:aead.NonceSize()], c.box[aead.NonceSize():], nil)
	if err != nil {
		return session{}, false
	}
	parts := strings.Split(string(plain), "\x00")
	if len(parts) != 3 {
		return session{}, false
	}
	return session{creds: awssig.Credentials{AccessKeyID: parts[0], SecretAccessKey: parts[1], SessionToken: parts[2]}, who: c.who, expires: c.expires}, true
}

func (d credDoc) cacheKey(region, sts string) string {
	_, key := processKey()
	h := hmac.New(sha256.New, key)
	for _, s := range []string{region, sts, d.AccessKeyID, d.SecretAccessKey, d.SessionToken, d.RoleARN, d.ExternalID, d.SessionName, strconv.Itoa(d.DurationSeconds)} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func mask(id string) string {
	if len(id) <= 8 {
		return "access-key"
	}
	return "access-key " + id[:4] + "…" + id[len(id)-4:]
}

// session returns what to sign with now: the key pair as given, or the role's
// temporary credentials, renewed when they are within refreshBefore of expiry.
func (p *Provider) session(ctx context.Context) (session, error) {
	raw, err := p.conn.Credential.Resolve()
	if err != nil {
		return session{}, err
	}
	d, err := parseCreds(raw)
	if err != nil {
		return session{}, fmt.Errorf("the connection %s: %w", p.conn.Name, err)
	}
	base := awssig.Credentials{AccessKeyID: d.AccessKeyID, SecretAccessKey: d.SecretAccessKey, SessionToken: d.SessionToken}
	if d.RoleARN == "" {
		return session{creds: base, who: mask(d.AccessKeyID)}, nil
	}
	key := d.cacheKey(p.ep.Region, p.ep.STS.Host)
	now := p.now()
	cacheMu.Lock()
	hit, ok := cacheMap[key]
	cacheMu.Unlock()
	if ok && now.Before(hit.expires.Add(-refreshBefore)) {
		if s, ok := hit.open(); ok {
			return s, nil
		}
	}
	s, err := p.assume(ctx, d, base)
	if err != nil {
		return session{}, err
	}
	cacheMu.Lock()
	if len(cacheMap) >= cacheLimit {
		for k, v := range cacheMap { // drop what is expired; if none is, start over
			if now.After(v.expires) {
				delete(cacheMap, k)
			}
		}
		if len(cacheMap) >= cacheLimit {
			cacheMap = map[string]sealedSession{}
		}
	}
	cacheMap[key] = seal(s)
	cacheMu.Unlock()
	return s, nil
}

// stsVersion is the Version of the token service's query API
// (https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html, "Version=2011-06-15").
const stsVersion = "2011-06-15"

type assumeResponse struct {
	Result struct {
		User struct {
			ARN string `xml:"Arn"`
		} `xml:"AssumedRoleUser"`
		Creds struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"Credentials"`
	} `xml:"AssumeRoleResult"`
}

type stsError struct {
	Error struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

// assume calls STS AssumeRole with the key pair (a signed GET, the form of the
// reference's example request; service "sts") and reads the XML answer.
func (p *Provider) assume(ctx context.Context, d credDoc, base awssig.Credentials) (session, error) {
	if p.ep.FIPS && !p.ep.Test {
		return session{}, errors.New("assuming a role through a FIPS endpoint is not built: use a connection to the regular endpoint, or give the temporary credentials as access_key_id, secret_access_key and session_token")
	}
	name := d.SessionName
	if name == "" {
		name = "launchway"
	}
	q := []string{"Action", "AssumeRole", "RoleArn", d.RoleARN, "RoleSessionName", name, "Version", stsVersion}
	if d.ExternalID != "" {
		q = append(q, "ExternalId", d.ExternalID)
	}
	if d.DurationSeconds != 0 {
		q = append(q, "DurationSeconds", strconv.Itoa(d.DurationSeconds))
	}
	cl := restkit.NewClient(restkit.Endpoints{API: p.ep.STS, Hosts: map[string]bool{p.ep.STS.Host: true}}, p.g,
		func() (string, error) { return base.SecretAccessKey, nil }, nil)
	cl.Sign = func(req *http.Request, _ []byte) error {
		awssig.Signer{Creds: base, Region: p.ep.Region, Service: "sts"}.Sign(req, awssig.EmptySHA256, p.now())
		return nil
	}
	a, err := cl.Do(ctx, http.MethodGet, cl.URL("/")+"?"+awssig.EncodeQuery(q...), nil, 0, "")
	if err != nil {
		return session{}, err
	}
	if a.Status != http.StatusOK {
		var e stsError
		_ = xml.NewDecoder(bytes.NewReader(a.Body)).Decode(&e)
		msg := p.scrub(e.Error.Message, base)
		return session{}, fmt.Errorf("the token service refused to assume the role (%d %s): %s. The key pair needs permission to call sts:AssumeRole on the role, and the role's trust policy must trust it", a.Status, p.scrub(e.Error.Code, base), msg)
	}
	var r assumeResponse
	if err := xml.NewDecoder(bytes.NewReader(a.Body)).Decode(&r); err != nil || r.Result.Creds.AccessKeyID == "" || r.Result.Creds.SessionToken == "" {
		return session{}, errors.New("the token service's answer is not what its API sends")
	}
	exp, err := time.Parse(time.RFC3339, r.Result.Creds.Expiration)
	if err != nil {
		return session{}, errors.New("the token service's answer names no expiry")
	}
	if now := p.now(); !exp.After(now) {
		return session{}, errors.New("the token service's answer names an expiry that has passed")
	} else if exp.After(now.Add(maxSessionLife)) {
		exp = now.Add(maxSessionLife) // never trust a session longer than the longest a role may have
	}
	who := r.Result.User.ARN
	if who == "" {
		who = "assumed role"
	}
	return session{creds: awssig.Credentials{AccessKeyID: r.Result.Creds.AccessKeyID, SecretAccessKey: r.Result.Creds.SecretAccessKey,
		SessionToken: r.Result.Creds.SessionToken}, who: who, expires: exp.UTC()}, nil
}
