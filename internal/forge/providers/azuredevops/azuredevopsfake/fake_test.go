package azuredevopsfake

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func send(t *testing.T, h *Host, method, path, body, auth string) int {
	t.Helper()
	req, _ := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The fake refuses what the documentation does not allow, and records each as a violation.
func TestTheFakeIsStrict(t *testing.T) {
	good := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+Token))
	repo := "/acme/proj/_apis/git/repositories/app"
	for name, c := range map[string]struct {
		method, path, body, auth string
		code                     int
	}{
		"no credential":        {"GET", "/acme/_apis/projects?api-version=7.1", "", "", 401},
		"a user name":          {"GET", "/acme/_apis/projects?api-version=7.1", "", "Basic " + base64.StdEncoding.EncodeToString([]byte("u:"+Token)), 401},
		"a bearer token":       {"GET", "/acme/_apis/projects?api-version=7.1", "", "Bearer " + Token, 401},
		"no api-version":       {"GET", "/acme/_apis/projects", "", good, 400},
		"another api-version":  {"GET", "/acme/_apis/projects?api-version=5.0", "", good, 400},
		"an unknown path":      {"GET", "/acme/_apis/nothing?api-version=7.1", "", good, 404},
		"a wrong method":       {"DELETE", repo + "?api-version=7.1", "", good, 404},
		"an unknown parameter": {"GET", repo + "/refs?api-version=7.1&bogus=1", "", good, 400},
		"an unknown field":     {"POST", repo + "/commits/" + strings.Repeat("a", 40) + "/statuses?api-version=7.1", `{"state":"pending","context":{"name":"n"},"extra":1}`, good, 400},
		"a bad state":          {"POST", repo + "/commits/" + strings.Repeat("a", 40) + "/statuses?api-version=7.1", `{"state":"ok","context":{"name":"n"}}`, good, 400},
		"a missing field":      {"POST", repo + "/annotatedtags?api-version=7.1", `{"name":"v1"}`, good, 400},
		"the draft flag":       {"PATCH", repo + "/pullrequests/1?api-version=7.1", `{"isDraft":false}`, good, 404},
		"not json":             {"POST", repo + "/pushes?api-version=7.1", `{`, good, 400},
	} {
		h := New()
		if got := send(t, h, c.method, c.path, c.body, c.auth); got != c.code {
			t.Errorf("%s: %d, want %d", name, got, c.code)
		}
		if len(h.Violations) == 0 && c.code != 404 || len(h.Violations) == 0 && name == "an unknown path" {
			t.Errorf("%s: not recorded as a violation", name)
		}
		h.Close()
	}
}
