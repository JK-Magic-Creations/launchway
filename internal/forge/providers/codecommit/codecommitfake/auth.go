package codecommitfake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
)

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// skew is how far the request's clock may be from the fake's: 15 minutes, the
// age after which the host says "RequestExpired" (the common errors page).
const skew = 15 * time.Minute

var authRE = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([A-Z0-9]+)/(\d{8})/([a-z0-9-]+)/([a-z0-9]+)/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

// caller is who a verified request is from.
type caller struct{ key, arn string }

// lookup finds the secret of an access key, checking the session token: a
// temporary key needs its token (and not be expired), a static one has none.
func (h *Host) lookup(key, token string) (secret, arn string, status int, typ, msg string) {
	if key == AccessKeyID {
		if token != "" {
			return "", "", http.StatusForbidden, "UnrecognizedClientException", "a session token was sent with a key that has none"
		}
		return SigningKey, "arn:aws:iam::" + Account + ":user/launchway", 0, "", ""
	}
	h.mu.Lock()
	t, ok := h.temp[key]
	h.mu.Unlock()
	switch {
	case !ok:
		return "", "", http.StatusForbidden, "UnrecognizedClientException", "the security token included in the request is invalid"
	case token != t.token:
		return "", "", http.StatusForbidden, "UnrecognizedClientException", "the security token does not belong to the key"
	case !h.now().Before(t.expires):
		return "", "", http.StatusForbidden, "ExpiredTokenException", "the security token included in the request has expired"
	}
	return t.secret, t.arn, 0, "", ""
}

// verify recomputes the request's signature. The service and region are what
// the scope must say.
func (h *Host) verify(w http.ResponseWriter, r *http.Request, body []byte, service string) (caller, bool) {
	m := authRE.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		h.violate(w, http.StatusForbidden, "IncompleteSignature", "the Authorization header is missing or not Signature Version 4: %q", "(not shown)")
		return caller{}, false
	}
	key, day, region, svc, signed, sig := m[1], m[2], m[3], m[4], strings.Split(m[5], ";"), m[6]
	date, err := time.Parse(awssig.DateFormat, r.Header.Get("X-Amz-Date"))
	switch {
	case err != nil:
		h.violate(w, http.StatusForbidden, "IncompleteSignature", "X-Amz-Date is missing or malformed")
		return caller{}, false
	case date.Format("20060102") != day:
		h.violate(w, http.StatusForbidden, "InvalidSignatureException", "the scope's date is not X-Amz-Date's")
		return caller{}, false
	case region != Region || svc != service:
		h.violate(w, http.StatusForbidden, "InvalidSignatureException", "the scope is %s/%s, want %s/%s", region, svc, Region, service)
		return caller{}, false
	case d(h.now().Sub(date)) > skew:
		h.violate(w, http.StatusBadRequest, "RequestExpired", "the request's time is more than 15 minutes from the server's")
		return caller{}, false
	}
	need := map[string]bool{"host": true, "x-amz-date": true}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		need["x-amz-security-token"] = true
	}
	if service == "codecommit" {
		need["x-amz-target"], need["content-type"] = true, true
	}
	for _, s := range signed {
		delete(need, s)
	}
	if len(need) > 0 {
		h.violate(w, http.StatusForbidden, "InvalidSignatureException", "signed headers lack %v", keys(need))
		return caller{}, false
	}
	if !sort.StringsAreSorted(signed) {
		h.violate(w, http.StatusForbidden, "InvalidSignatureException", "signed headers are not sorted")
		return caller{}, false
	}
	secret, arn, st, typ, msg := h.lookup(key, r.Header.Get("X-Amz-Security-Token"))
	if st != 0 {
		h.violate(w, st, typ, "%s", msg)
		return caller{}, false
	}
	var hb strings.Builder
	for _, name := range signed {
		v := r.Header.Get(name)
		if name == "host" {
			v = r.Host
		}
		hb.WriteString(name + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	uri := r.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	canonical := strings.Join([]string{r.Method, uri, awssig.CanonicalQuery(r.URL.RawQuery), hb.String(), m[5], awssig.HexSHA256(body)}, "\n")
	toSign := awssig.Algorithm + "\n" + r.Header.Get("X-Amz-Date") + "\n" + awssig.Scope(day, region, svc) + "\n" + awssig.HexSHA256([]byte(canonical))
	if !hmac.Equal([]byte(awssig.Signing(secret, day, region, svc, toSign)), []byte(sig)) {
		h.violate(w, http.StatusForbidden, "InvalidSignatureException", "the request signature we calculated does not match the signature you provided")
		return caller{}, false
	}
	return caller{key: key, arn: arn}, true
}

func d(x time.Duration) time.Duration {
	if x < 0 {
		return -x
	}
	return x
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readBody(r *http.Request) []byte {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	return b
}

// checkGit verifies the user name and password of a Git request for the repository
// path: the signature of "GIT\n<path>\n\nhost:<host>\n\nhost\n".
func (h *Host) checkGit(user, pass, host, path string) (string, bool) {
	key, token, _ := strings.Cut(user, "%")
	ts, sig, ok := strings.Cut(pass, "Z")
	if !ok || len(ts) != 15 || len(sig) != 64 {
		return "the password is not a timestamp, a Z and a signature", false
	}
	at, err := time.Parse("20060102T150405", ts)
	if err != nil || d(h.now().Sub(at)) > skew {
		return "the password's timestamp is not within 15 minutes", false
	}
	secret, _, st, _, msg := h.lookup(key, token)
	if st != 0 {
		return msg, false
	}
	canonical := "GIT\n" + path + "\n\nhost:" + host + "\n\nhost\n"
	toSign := awssig.Algorithm + "\n" + ts + "\n" + awssig.Scope(ts[:8], Region, "codecommit") + "\n" + hex.EncodeToString(sum(canonical))
	if !hmac.Equal([]byte(awssig.Signing(secret, ts[:8], Region, "codecommit", toSign)), []byte(sig)) {
		return "the signature does not match", false
	}
	return "", true
}

// CheckGit says whether a user name and password are good for the repository's clone address on this fake.
func (h *Host) CheckGit(user, pass string) bool {
	_, ok := h.checkGit(user, pass, h.hostname(), "/v1/repos/"+Repo)
	return ok
}

func (h *Host) hostname() string {
	u := strings.TrimPrefix(h.URL, "http://")
	host, _, _ := strings.Cut(u, ":")
	return host
}

func sum(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }
