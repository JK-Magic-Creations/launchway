package codecommitfake

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
)

// call sends a request signed with the fake's key pair (sign) at the time given.
func call(t *testing.T, h *Host, method, path, target, ctype, body string, sign func(*http.Request, string)) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if target != "" {
		req.Header.Set("X-Amz-Target", target)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if sign != nil {
		sign(req, body)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func signer(secret string, at time.Time) func(*http.Request, string) {
	return func(r *http.Request, body string) {
		awssig.Signer{Creds: awssig.Credentials{AccessKeyID: AccessKeyID, SecretAccessKey: secret}, Region: Region, Service: "codecommit"}.Sign(r, awssig.HexSHA256([]byte(body)), at)
	}
}

const jsonType = "application/x-amz-json-1.1"

// The fake refuses what the documentation does not allow, and records each as a violation.
func TestTheFakeIsStrict(t *testing.T) {
	now := time.Now()
	good := signer(SigningKey, now)
	for name, c := range map[string]struct {
		method, path, target, ctype, body string
		sign                              func(*http.Request, string)
		code                              int
	}{
		"no signature":          {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", nil, 403},
		"a wrong secret":        {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", signer("wrong", now), 403},
		"a stale request":       {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", signer(SigningKey, now.Add(-time.Hour)), 400},
		"an unknown action":     {"POST", "/", "CodeCommit_20150413.Nothing", jsonType, "{}", good, 404},
		"another service":       {"POST", "/", "Other_1.ListRepositories", jsonType, "{}", good, 404},
		"a wrong method":        {"GET", "/", "CodeCommit_20150413.ListRepositories", jsonType, "", good, 404},
		"a wrong path":          {"POST", "/x", "CodeCommit_20150413.ListRepositories", jsonType, "{}", good, 404},
		"a wrong content type":  {"POST", "/", "CodeCommit_20150413.ListRepositories", "application/json", "{}", good, 400},
		"an unknown field":      {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, `{"bogus":1}`, good, 400},
		"a missing field":       {"POST", "/", "CodeCommit_20150413.GetCommit", jsonType, `{"repositoryName":"app"}`, good, 400},
		"a number for a string": {"POST", "/", "CodeCommit_20150413.GetPullRequest", jsonType, `{"pullRequestId":1}`, good, 400},
		"a bad repository name": {"POST", "/", "CodeCommit_20150413.ListBranches", jsonType, `{"repositoryName":"a b"}`, good, 400},
		"a too long title":      {"POST", "/", "CodeCommit_20150413.UpdatePullRequestTitle", jsonType, `{"pullRequestId":"1","title":"` + strings.Repeat("x", 151) + `"}`, good, 400},
		"not json":              {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, `{`, good, 400},
		"a bad status":          {"POST", "/", "CodeCommit_20150413.ListPullRequests", jsonType, `{"repositoryName":"app","pullRequestStatus":"MERGED"}`, good, 400},
		"a token with a static key": {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", func(r *http.Request, b string) {
			awssig.Signer{Creds: awssig.Credentials{AccessKeyID: AccessKeyID, SecretAccessKey: SigningKey, SessionToken: "tok"}, Region: Region, Service: "codecommit"}.Sign(r, awssig.HexSHA256([]byte(b)), now)
		}, 403},
		"another region": {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", func(r *http.Request, b string) {
			awssig.Signer{Creds: awssig.Credentials{AccessKeyID: AccessKeyID, SecretAccessKey: SigningKey}, Region: "eu-west-1", Service: "codecommit"}.Sign(r, awssig.HexSHA256([]byte(b)), now)
		}, 403},
		"a body that is not the one signed": {"POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, `{"order":"ascending"}`, func(r *http.Request, _ string) {
			signer(SigningKey, now)(r, "{}")
		}, 403},
		"a git request without credentials":    {"GET", "/v1/repos/app/info/refs?service=git-upload-pack", "", "", "", nil, 401},
		"a git request for another repository": {"GET", "/v1/repos/other/info/refs?service=git-upload-pack", "", "", "", nil, 404},
		"an unknown sts parameter": {"GET", "/?Action=AssumeRole&Version=2011-06-15&RoleArn=x&RoleSessionName=ab&Bogus=1", "", "", "", func(r *http.Request, _ string) {
			awssig.Signer{Creds: awssig.Credentials{AccessKeyID: AccessKeyID, SecretAccessKey: SigningKey}, Region: Region, Service: "sts"}.Sign(r, awssig.EmptySHA256, now)
		}, 400},
	} {
		h := New()
		code, body := call(t, h, c.method, c.path, c.target, c.ctype, c.body, c.sign)
		if code != c.code {
			t.Errorf("%s: %d, want %d (%s)", name, code, c.code, body)
		}
		if len(h.Violations) == 0 {
			t.Errorf("%s: not recorded as a violation", name)
		}
		h.Close()
	}
}

func TestAGoodCallIsAnswered(t *testing.T) {
	h := New()
	defer h.Close()
	code, body := call(t, h, "POST", "/", "CodeCommit_20150413.ListRepositories", jsonType, "{}", signer(SigningKey, time.Now()))
	if code != 200 || !strings.Contains(body, `"repositoryName":"app"`) || len(h.Violations) != 0 {
		t.Fatalf("%d %s %v", code, body, h.Violations)
	}
}
