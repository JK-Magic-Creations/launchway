package azuredevops

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/providers/azuredevops/azuredevopsfake"
)

func h40(c string) string { return strings.Repeat(c, 40) }

const containers = `"resourceContainers":{"collection":{"baseUrl":"https://dev.azure.com/acme/"},"account":{"baseUrl":"https://dev.azure.com/acme/"}}`

func pushBody(id string) string {
	return `{"id":"` + id + `","eventType":"git.push","createdDate":"2026-01-02T03:04:05Z","resource":{"refUpdates":[` +
		`{"name":"refs/heads/main","oldObjectId":"` + h40("b") + `","newObjectId":"` + h40("a") + `"},` +
		`{"name":"refs/tags/v1","oldObjectId":"` + h40("0") + `","newObjectId":"` + h40("c") + `"},` +
		`{"name":"refs/heads/gone","oldObjectId":"` + h40("d") + `","newObjectId":"` + h40("0") + `"}],` +
		`"repository":{"name":"app","project":{"name":"proj"}},"pushedBy":{"displayName":"Bob","uniqueName":"bob@example.com"}},` + containers + `}`
}

func pullBody(event, status string, fork bool) string {
	fs := ""
	if fork {
		fs = `"forkSource":{"repository":{"name":"app","project":{"name":"eve"}}},`
	}
	return `{"id":"n-7","eventType":"` + event + `","resource":{"pullRequestId":7,"status":"` + status + `","title":"T","isDraft":true,` + fs +
		`"createdBy":{"displayName":"Eve","uniqueName":"eve@example.com"},"sourceRefName":"refs/heads/f","targetRefName":"refs/heads/main",` +
		`"lastMergeSourceCommit":{"commitId":"` + h40("b") + `"},` +
		`"repository":{"name":"app","remoteUrl":"https://acme@dev.azure.com/acme/proj/_git/app","project":{"name":"proj"}}},` + containers + `}`
}

func deliver(f *fixture, body string, hdr map[string]string) ([]forge.Event, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return f.p.Events().Verify(req, []byte(azuredevopsfake.WebhookSecret))
}

func basic(pw string) map[string]string {
	return map[string]string{"Authorization": azuredevopsfake.Basic("launchway", pw)}
}

// A service hook has no signature: it is verified by the secret the subscription sends, as the password of basic
// authentication or in an HTTP header, and a delivery with neither or a wrong one is refused.
func TestWebhookIsVerifiedBothWaysAndRefusedWithoutTheSecret(t *testing.T) {
	f := newFixture(t)
	body := pushBody("n-1")
	for name, hdr := range map[string]map[string]string{
		"basic authentication":                   basic(azuredevopsfake.WebhookSecret),
		"a header":                               {HookAuthHeader: azuredevopsfake.WebhookSecret},
		"both":                                   {HookAuthHeader: azuredevopsfake.WebhookSecret, "Authorization": azuredevopsfake.Basic("x", azuredevopsfake.WebhookSecret)},
		"a right header beside a wrong password": {HookAuthHeader: azuredevopsfake.WebhookSecret, "Authorization": azuredevopsfake.Basic("x", "nope")},
	} {
		if evs, err := deliver(f, body, hdr); err != nil || len(evs) != 3 {
			t.Errorf("%s: %v %v", name, evs, err)
		}
	}
	for name, hdr := range map[string]map[string]string{
		"nothing":               {},
		"a wrong password":      basic("wrong"),
		"an empty password":     basic(""),
		"a wrong header":        {HookAuthHeader: "wrong"},
		"an empty header":       {HookAuthHeader: ""},
		"the secret as a user":  {"Authorization": azuredevopsfake.Basic(azuredevopsfake.WebhookSecret, "")},
		"a longer wrong secret": basic(azuredevopsfake.WebhookSecret + "x"),
		"a prefix":              basic(azuredevopsfake.WebhookSecret[:3]),
		"a bearer token":        {"Authorization": "Bearer " + azuredevopsfake.WebhookSecret},
	} {
		if evs, err := deliver(f, body, hdr); !errors.Is(err, ErrBadCredential) || len(evs) != 0 {
			t.Errorf("%s: %v %v", name, evs, err)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "http://x/hook", bytes.NewBufferString(body))
	req.SetBasicAuth("u", "p")
	if _, err := f.p.Events().Verify(req, nil); err == nil {
		t.Error("a connection with no webhook secret accepts nothing")
	}
	if !strings.Contains(Capabilities()[forge.CapEvents].Reason, "weaker than a signature") {
		t.Error("the weaker check is declared")
	}
}

// A replay carries the same notification id, so its events carry the same delivery ids (the service drops the
// second one: see the journey in the hosts package); another notification has others.
func TestWebhookDeliveryIdsMakeAReplayRecognisable(t *testing.T) {
	f := newFixture(t)
	hdr := basic(azuredevopsfake.WebhookSecret)
	a, err := deliver(f, pushBody("n-1"), hdr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := deliver(f, pushBody("n-1"), hdr)
	c, _ := deliver(f, pushBody("n-2"), hdr)
	if a[0].DeliveryID != b[0].DeliveryID || a[0].DeliveryID == c[0].DeliveryID || a[0].DeliveryID == a[1].DeliveryID {
		t.Errorf("%q %q %q %q", a[0].DeliveryID, b[0].DeliveryID, c[0].DeliveryID, a[1].DeliveryID)
	}
	// No id in the payload: the notification number, else the body.
	noID := strings.Replace(pushBody("n-1"), `"id":"n-1",`, `"notificationId":4,`, 1)
	if evs, _ := deliver(f, noID, hdr); len(evs) == 0 || !strings.HasPrefix(evs[0].DeliveryID, "n4-git.push") {
		t.Errorf("%+v", evs)
	}
	bare := strings.Replace(pushBody("n-1"), `"id":"n-1",`, ``, 1)
	if evs, _ := deliver(f, bare, hdr); len(evs) == 0 || !strings.HasPrefix(evs[0].DeliveryID, "body-") {
		t.Errorf("%+v", evs)
	}
}

func TestWebhookPushAndPullRequestEvents(t *testing.T) {
	f := newFixture(t)
	hdr := basic(azuredevopsfake.WebhookSecret)
	evs, err := deliver(f, pushBody("n-1"), hdr)
	if err != nil || len(evs) != 3 || evs[0].Ref != "refs/heads/main" || evs[0].Commit != h40("a") || evs[0].Old != h40("b") || evs[1].Ref != "refs/tags/v1" || evs[1].Old != h40("0") ||
		evs[2].Ref != "refs/heads/gone" || evs[2].Commit != h40("0") || evs[0].Repository != "acme/proj/app" || evs[0].Actor.Login != "bob@example.com" || evs[0].Kind != forge.EventRefUpdated {
		t.Fatalf("%+v %v", evs, err)
	}
	for _, c := range []struct {
		event, status string
		fork          bool
		kind          forge.EventKind
	}{
		{"git.pullrequest.created", "active", false, forge.EventProposalOpened},
		{"git.pullrequest.updated", "active", true, forge.EventProposalRevised},
		{"git.pullrequest.updated", "completed", false, forge.EventProposalLanded},
		{"git.pullrequest.updated", "abandoned", false, forge.EventProposalClosed},
		{"git.pullrequest.merged", "active", false, forge.EventProposalRevised},
	} {
		evs, err := deliver(f, pullBody(c.event, c.status, c.fork), hdr)
		if err != nil || len(evs) != 1 || evs[0].Kind != c.kind || evs[0].Fork != c.fork || evs[0].Number != 7 || !evs[0].Draft || evs[0].Head != h40("b") || evs[0].Base != "main" ||
			evs[0].Author != "eve@example.com" || evs[0].Repository != "acme/proj/app" || evs[0].URL != "https://dev.azure.com/acme/proj/_git/app/pullrequest/7" {
			t.Errorf("%s %s: %+v %v", c.event, c.status, evs, err)
		}
	}
	for _, other := range []string{"ms.vss-code.git-pullrequest-comment-event", "build.complete"} {
		if evs, err := deliver(f, `{"id":"x","eventType":"`+other+`","resource":{}}`, hdr); err != nil || len(evs) != 0 {
			t.Errorf("%s starts nothing: %v %v", other, evs, err)
		}
	}
	for _, bad := range []string{`{}`, `not json`, `{"id":"x","eventType":"git.push","resource":{}}`} {
		if _, err := deliver(f, bad, hdr); err == nil {
			t.Errorf("%q is not what the host sends", bad)
		}
	}
	// The organization comes from the collection, or from the first label of an old-style address.
	old := strings.Replace(pushBody("n-1"), "https://dev.azure.com/acme/", "https://acme.visualstudio.com/", 2)
	if evs, _ := deliver(f, old, hdr); len(evs) == 0 || evs[0].Repository != "acme/proj/app" {
		t.Errorf("%+v", evs)
	}
}
