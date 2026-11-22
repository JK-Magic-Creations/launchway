package codecommit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/codecommit/codecommitfake"
)

// The temporary credentials of an assumed role are held sealed: the secret key and the token are not
// in the bytes the cache keeps, and they come back whole for the next call.
func TestAssumedSessionsAreHeldSealedInMemory(t *testing.T) {
	f := newFixture(t, codecommitfake.RoleCredentialDoc(), codecommitfake.Account)
	s, err := f.p.session(t.Context())
	if err != nil || s.creds.SecretAccessKey == "" || s.creds.SessionToken == "" {
		t.Fatalf("%+v %v", s, err)
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	found := false
	for _, c := range cacheMap {
		if bytes.Contains(c.box, []byte(s.creds.SecretAccessKey)) || bytes.Contains(c.box, []byte(s.creds.SessionToken)) || bytes.Contains(c.box, []byte(s.creds.AccessKeyID)) {
			t.Fatal("the cache holds temporary credentials in the clear")
		}
		if back, ok := c.open(); ok && back.creds == s.creds {
			found = true
		}
	}
	if !found {
		t.Error("the sealed session does not open to what was assumed")
	}
	// Tampering is detected, not used.
	for _, c := range cacheMap {
		c.box[len(c.box)-1] ^= 1
		if _, ok := c.open(); ok {
			t.Error("a changed sealed session is opened")
		}
		c.box[len(c.box)-1] ^= 1
	}
	for k := range cacheMap {
		if strings.Contains(k, codecommitfake.SigningKey) {
			t.Error("a cache key holds the secret key")
		}
	}
}

// The role session is named launchway unless the operator names it, so it carries no person's name, and its
// life is bounded by what a role can have, whatever expiry the answer names.
func TestTheRoleSessionNameAndLifeAreBounded(t *testing.T) {
	doc := strings.Replace(codecommitfake.RoleCredentialDoc(), `,"role_session_name":"launchway"`, "", 1)
	f := newFixture(t, doc, codecommitfake.Account)
	s, err := f.p.session(t.Context())
	if err != nil || !strings.HasSuffix(s.who, "/launchway") {
		t.Fatalf("%q %v", s.who, err)
	}
	for _, bad := range []string{`"role_session_name":"a b"`, `"role_session_name":"x"`, `"duration_seconds":86400`, `"duration_seconds":899`} {
		d := strings.TrimSuffix(codecommitfake.RoleCredentialDoc(), "}")
		d = strings.Replace(d, `,"role_session_name":"launchway"`, "", 1) + "," + bad + "}"
		if _, err := parseCreds(d); err == nil {
			t.Errorf("%s is accepted", bad)
		}
	}
	// A far-future expiry is cut to twelve hours.
	f.h.SessionSeconds(43200)
	now := time.Now().UTC()
	f.p.SetClock(func() time.Time { return now })
	t.Setenv("LW_TEST_CC_CRED", strings.Replace(codecommitfake.RoleCredentialDoc(), `"launchway"`, `"cap-test"`, 1))
	s, err = f.p.session(t.Context())
	if err != nil || s.expires.After(now.Add(maxSessionLife).Add(time.Second)) {
		t.Errorf("%v %v", s.expires, err)
	}
}

// What a host says in an error is returned without the credentials, as one short line.
func TestAHostsErrorWordsAreScrubbedOfTheCredentials(t *testing.T) {
	const secret, token = "test-only-secret-value-0001", "test-only-session-token-0002"
	doc := `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"` + secret + `","session_token":"` + token + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(http.StatusBadRequest)
		msg := "bad " + secret + " and " + token + " see https://u:pw@h/x?token=zzz " + strings.Repeat("long ", 500)
		b, _ := json.Marshal(map[string]string{"__type": "com.amazonaws#InvalidX", "message": msg})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	t.Setenv("LW_TEST_CC_CRED", doc)
	restore := UseTestAPI(srv.URL)
	ep, err := EndpointsOf("")
	restore()
	if err != nil {
		t.Fatal(err)
	}
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "c", Name: "cc", Kind: forge.KindCodeCommit, Credential: forge.SecretRef{Env: "LW_TEST_CC_CRED"}}, ep, g)
	_, err = p.Repositories().List(t.Context(), "")
	if err == nil {
		t.Fatal("no error")
	}
	for _, leak := range []string{secret, token, "u:pw@", "token=zzz"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error holds %q", leak)
		}
	}
	if strings.Contains(err.Error(), "\n") || len(err.Error()) > 700 {
		t.Errorf("the error is %d characters", len(err.Error()))
	}
}

// The token service's refusal is scrubbed the same way.
func TestTheTokenServicesWordsAreScrubbedToo(t *testing.T) {
	const secret = "test-only-secret-value-0003"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>AccessDenied</Code><Message>key ` + secret + ` is not allowed</Message></Error></ErrorResponse>`))
	}))
	defer srv.Close()
	doc := `{"access_key_id":"AKFAFAKEFAKEFAKE0001","secret_access_key":"` + secret + `","role_arn":"arn:aws:iam::123456789012:role/r"}`
	t.Setenv("LW_TEST_CC_CRED", doc)
	restore := UseTestAPI(srv.URL)
	ep, _ := EndpointsOf("")
	restore()
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "c", Name: "cc", Kind: forge.KindCodeCommit, Credential: forge.SecretRef{Env: "LW_TEST_CC_CRED"}}, ep, g)
	_, err := p.session(t.Context())
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("%v", err)
	}
}

// A host that keeps sending a continuation token with nothing in it cannot hold a call forever.
func TestEndlessContinuationTokensStopTheListing(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"branches":[],"approvalRuleTemplateNames":[],"nextToken":"again"}`))
	}))
	defer srv.Close()
	t.Setenv("LW_TEST_CC_CRED", codecommitfake.CredentialDoc())
	restore := UseTestAPI(srv.URL)
	ep, _ := EndpointsOf("")
	restore()
	g, _ := egress.New([]string{"127.0.0.1"})
	p := NewAt(forge.Connection{ID: "c", Name: "cc", Kind: forge.KindCodeCommit, Credential: forge.SecretRef{Env: "LW_TEST_CC_CRED"}}, ep, g)
	repo := forge.Repository{Path: codecommitfake.Account + "/app"}
	if _, err := p.Content().Refs(t.Context(), repo); err == nil {
		t.Error("branches: no end")
	}
	if _, err := p.Rules().List(t.Context(), repo); err == nil {
		t.Error("rules: no end")
	}
	if n := calls.Load(); n > 2*maxPages+2 {
		t.Errorf("%d calls", n)
	}
}

// The maturity says why it is experimental, and the signing of the Git password is the reason.
func TestTheProviderIsExperimentalWithTheReasonDeclared(t *testing.T) {
	f := newFixture(t, codecommitfake.CredentialDoc(), codecommitfake.Account)
	r, ok := any(f.p).(interface{ MaturityReason() string })
	if f.p.Maturity() != forge.Experimental || !ok || !strings.Contains(r.MaturityReason(), "open-source") || !strings.Contains(r.MaturityReason(), "no documentation page") {
		t.Errorf("%v %v", f.p.Maturity(), ok)
	}
}

// Sanity: the Git password is made from the same signer the API uses.
func TestGitCredentialUsesTheSharedSigner(t *testing.T) {
	c := awssig.Credentials{AccessKeyID: "AKFAFAKEFAKEFAKE0001", SecretAccessKey: "k"}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	u, p := GitCredential(c, "us-east-1", "git-codecommit.us-east-1.amazonaws.com", "/v1/repos/x", at)
	if u != c.AccessKeyID || !strings.HasPrefix(p, "20260102T030405Z") {
		t.Errorf("%q %q", u, p)
	}
}
