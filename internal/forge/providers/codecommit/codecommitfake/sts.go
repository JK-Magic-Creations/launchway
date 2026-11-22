package codecommitfake

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	roleSessionRE = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)
	externalRE    = regexp.MustCompile(`^[\w+=,.@:/-]+$`)
)

// SessionSeconds makes the next AssumeRole sessions this long, whatever is asked (0: as asked, 3600 by default).
func (h *Host) SessionSeconds(n int) {
	h.mu.Lock()
	h.hours = n
	h.mu.Unlock()
}

func (h *Host) stsError(w http.ResponseWriter, status int, code, msg string, violation bool) {
	if violation {
		h.mu.Lock()
		h.Violations = append(h.Violations, code+": "+msg)
		h.mu.Unlock()
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string
			Message string
		}
	}{Error: struct{ Code, Message string }{code, msg}})
}

// serveSTS answers AssumeRole: a signed GET with the parameters of the
// reference (Action, Version 2011-06-15, RoleArn, RoleSessionName and optionally
// ExternalId and DurationSeconds) and no other.
func (h *Host) serveSTS(w http.ResponseWriter, r *http.Request) {
	who, ok := h.verify(w, r, nil, "sts")
	if !ok {
		return
	}
	h.record("STS AssumeRole")
	q := r.URL.Query()
	allowed := map[string]bool{"Action": true, "Version": true, "RoleArn": true, "RoleSessionName": true, "ExternalId": true, "DurationSeconds": true}
	for k := range q {
		if !allowed[k] {
			h.stsError(w, 400, "InvalidParameterValue", "AssumeRole has no parameter "+k, true)
			return
		}
	}
	switch {
	case q.Get("Action") != "AssumeRole":
		h.stsError(w, 400, "InvalidAction", "only AssumeRole is implemented", true)
		return
	case q.Get("Version") != "2011-06-15":
		h.stsError(w, 400, "InvalidParameterValue", "the Version is 2011-06-15", true)
		return
	case !roleSessionRE.MatchString(q.Get("RoleSessionName")) || q.Get("RoleArn") == "":
		h.stsError(w, 400, "ValidationError", "RoleArn and RoleSessionName (2 to 64 of [\\w+=,.@-]) are required", true)
		return
	case q.Get("ExternalId") != "" && (len(q.Get("ExternalId")) < 2 || len(q.Get("ExternalId")) > 1224 || !externalRE.MatchString(q.Get("ExternalId"))):
		h.stsError(w, 400, "ValidationError", "the ExternalId has characters not allowed", true)
		return
	}
	dur := 3600
	if v := q.Get("DurationSeconds"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 900 || n > 43200 {
			h.stsError(w, 400, "ValidationError", "DurationSeconds is 900 to 43200", true)
			return
		}
		dur = n
	}
	if who.key != AccessKeyID || q.Get("RoleArn") != RoleARN || (h.RequireExternalID && q.Get("ExternalId") != ExternalID) {
		h.stsError(w, http.StatusForbidden, "AccessDenied", "User: "+who.arn+" is not authorized to perform: sts:AssumeRole on "+q.Get("RoleArn"), false)
		return
	}
	h.mu.Lock()
	if h.hours > 0 {
		dur = h.hours
	}
	n := h.nextKey
	h.nextKey++
	h.Assumed++
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	exp := h.now().Add(time.Duration(dur) * time.Second).UTC()
	id := "ASIAFAKEFAKE" + strings.ToUpper(strconv.FormatInt(int64(1000+n), 36)) + "XXXX"
	cred := tempCred{secret: hex.EncodeToString(b), token: "FwoGZXIvYXdz" + hex.EncodeToString(b), expires: exp,
		arn: "arn:aws:sts::" + Account + ":assumed-role/launchway-ci/" + q.Get("RoleSessionName")}
	h.temp[id] = cred
	h.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	_, _ = w.Write([]byte(`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><AssumedRoleUser><Arn>` + cred.arn + //nolint:gosec // G705: a test fake answering XML with values it made or checked (the session name is validated against [\w+=,.@-])
		`</Arn><AssumedRoleId>ARO123EXAMPLE123:` + q.Get("RoleSessionName") + `</AssumedRoleId></AssumedRoleUser><Credentials><AccessKeyId>` + id + `</AccessKeyId><SecretAccessKey>` +
		cred.secret + `</SecretAccessKey><SessionToken>` + cred.token + `</SessionToken><Expiration>` + exp.Format("2006-01-02T15:04:05Z") +
		`</Expiration></Credentials></AssumeRoleResult><ResponseMetadata><RequestId>c6104cbe-af31-11e0-8154-cbc7ccf896c7</RequestId></ResponseMetadata></AssumeRoleResponse>`))
}
