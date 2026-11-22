package codecommit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/awssig"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// target is the prefix of X-Amz-Target: the service and its API version
// (every sample request of the API reference shows "CodeCommit_20150413.<Action>").
const target = "CodeCommit_20150413."

// jsonType is the content type of the JSON 1.1 protocol (the sample requests'
// "Content-Type: application/x-amz-json-1.1").
const jsonType = "application/x-amz-json-1.1"

// apiError is the host's error answer. The error name is in "__type" (the JSON
// protocols' convention) and the words in "message".
type apiError struct {
	Status int
	Type   string
	Msg    string
}

func (e *apiError) Error() string {
	t := e.Type
	if t != "" {
		t = " (" + t + ")"
	}
	m := e.Msg
	if m != "" {
		m = ": " + m
	}
	switch e.Status {
	case http.StatusForbidden:
		return fmt.Sprintf("the host answered 403%s%s. Either the credentials lack the IAM permission this needs on the repository (see the connection test for what each feature needs), or they have expired", t, m)
	case http.StatusNotFound:
		return fmt.Sprintf("the host answered 404%s%s", t, m)
	}
	return fmt.Sprintf("the host answered %d%s%s", e.Status, t, m)
}

// is says the error is one of the named kinds.
func (e *apiError) is(types ...string) bool {
	for _, t := range types {
		if e.Type == t {
			return true
		}
	}
	return false
}

// Unwrap makes the host's "no such ..." answers forge.ErrNotFound.
func (e *apiError) Unwrap() error {
	if e.is("RepositoryDoesNotExistException", "PullRequestDoesNotExistException", "BranchDoesNotExistException",
		"CommitIdDoesNotExistException", "CommitDoesNotExistException", "ApprovalRuleTemplateDoesNotExistException") {
		return forge.ErrNotFound
	}
	return nil
}

func asAPI(err error) (*apiError, bool) {
	var a *apiError
	return a, errors.As(err, &a)
}

func parseAPIError(status int, body []byte) *apiError {
	var in struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
		Upper   string `json:"Message"`
	}
	_ = json.Unmarshal(body, &in)
	t := in.Type
	if i := strings.LastIndex(t, "#"); i >= 0 {
		t = t[i+1:]
	}
	m := in.Message
	if m == "" {
		m = in.Upper
	}
	return &apiError{Status: status, Type: t, Msg: strings.Join(strings.Fields(m), " ")}
}

// message is the host's own words for a refusal (restkit's Message hook).
func message(body []byte) string {
	e := parseAPIError(0, body)
	if e.Type == "" {
		return e.Msg
	}
	return e.Type + ": " + e.Msg
}

// invoke calls one action: POST / with X-Amz-Target and a JSON 1.1 body (in
// may be nil for {}), and decodes the answer into out when it is not nil.
func (p *Provider) invoke(ctx context.Context, action string, in, out any) error {
	if in == nil {
		in = struct{}{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	a, err := p.c.DoWith(ctx, http.MethodPost, p.c.URL("/"), bytes.NewReader(b), int64(len(b)), jsonType, http.Header{"X-Amz-Target": {target + action}})
	if err != nil {
		return err
	}
	if a.Status != http.StatusOK {
		e := parseAPIError(a.Status, a.Body)
		var held []awssig.Credentials
		if s, err := p.session(ctx); err == nil {
			held = append(held, s.creds)
		}
		e.Msg, e.Type = p.scrub(e.Msg, held...), p.scrub(e.Type, held...)
		return e
	}
	if out != nil && len(a.Body) > 0 {
		if err := json.Unmarshal(a.Body, out); err != nil {
			return errors.New("the host's answer is not what its API sends")
		}
	}
	return nil
}

// epoch reads a timestamp as the API sends it: seconds since the epoch, with a fraction.
func epoch(f float64) time.Time {
	if f <= 0 {
		return time.Time{}
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)).UTC()
}

// gitDate reads a commit's date as GetCommit sends it ("1484167798 -0800": seconds, then the zone).
func gitDate(s string) time.Time {
	f, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	n, err := strconv.ParseInt(f, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// token is a fresh idempotency token for a request that makes something.
func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// login is the user or role name at the end of an ARN
// ("arn:aws:iam::123456789012:user/Mary" gives "Mary"; "...:assumed-role/Role/session" gives "Role/session").
func login(arn string) string {
	_, rest, ok := strings.Cut(arn, ":user/")
	if !ok {
		_, rest, ok = strings.Cut(arn, ":assumed-role/")
	}
	if !ok {
		return arn
	}
	return rest
}

// scrub makes the host's own words safe to return to a caller: the secret
// access keys and session tokens of the credential (and of any given
// credentials) are blanked, addresses lose their credentials, and the text is
// one line of at most 400 characters. Every message that came from a host's
// answer goes through here.
func (p *Provider) scrub(msg string, held ...awssig.Credentials) string {
	secrets := []string{}
	if raw, err := p.conn.Credential.Resolve(); err == nil {
		if d, err := parseCreds(raw); err == nil {
			secrets = append(secrets, d.SecretAccessKey, d.SessionToken)
		}
	}
	for _, c := range held {
		secrets = append(secrets, c.SecretAccessKey, c.SessionToken)
	}
	for _, s := range secrets {
		if len(s) > 3 {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	msg = strings.Join(strings.Fields(redact.URLs(msg)), " ")
	if r := []rune(msg); len(r) > 400 {
		msg = string(r[:400]) + "…"
	}
	return msg
}
