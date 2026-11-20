package testreport

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file holds what every parser uses to fill the richer model (tags,
// properties, steps, attachments, attempts, HTTP exchanges) within its
// bounds. A parser builds a Case and hands each piece to the collector,
// which cleans it, cuts it to the limits and notes the first cut of each
// kind in the report's warning.

// Bounds of short fields that are not configurable.
const (
	maxKeywordBytes     = 100
	maxContentTypeBytes = 200
	maxMethodBytes      = 20
	maxRetries          = 1000 // a retry count written in a file is cut to this
)

// hidden replaces a credential.
const hidden = "[hidden]"

// warnOnce records a warning the first time a kind of cut happens.
func (c *collector) warnOnce(kind, format string, args ...any) {
	if c.once == nil {
		c.once = map[string]bool{}
	}
	if c.once[kind] {
		return
	}
	c.once[kind] = true
	c.warn(format, args...)
}

// cleanTag cleans a tag and drops one leading @.
func cleanTag(s string) string {
	t := cleanName(s, 4*MaxTagBytes)
	t = strings.TrimSpace(strings.TrimPrefix(t, "@"))
	return cutHead(t, MaxTagBytes, false)
}

// addTags adds tags to the case, without duplicates, within MaxTags.
func (c *collector) addTags(cs *Case, tags ...string) {
	for _, raw := range tags {
		t := cleanTag(raw)
		if t == "" {
			continue
		}
		dup := false
		for _, have := range cs.Tags {
			if have == t {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if len(cs.Tags) >= MaxTags {
			c.warnOnce("tags", "some cases have more than %d tags; the rest were left out", MaxTags)
			return
		}
		cs.Tags = append(cs.Tags, t)
	}
}

// addProperty adds a name/value property within MaxProperties. Properties
// with no name are dropped.
func (c *collector) addProperty(cs *Case, name, value string) {
	n := cleanName(name, MaxPropertyName)
	if n == "" {
		return
	}
	if len(cs.Properties) >= MaxProperties {
		c.warnOnce("properties", "some cases have more than %d properties; the rest were left out", MaxProperties)
		return
	}
	cs.Properties = append(cs.Properties, Property{Name: n, Value: cleanMessage(value, MaxPropertyText, false)})
}

// setVariant sets the case's variant, cleaned and cut.
func setVariant(cs *Case, v string) {
	cs.Variant = cleanName(v, MaxVariantBytes)
}

// setRetries sets the retry count, never trusting more than maxRetries.
func setRetries(cs *Case, n int) {
	cs.Retries = min(max(n, 0), maxRetries)
}

// addAttempt adds an earlier try within MaxAttempts.
func (c *collector) addAttempt(cs *Case, st Status, ms int64, msg string) {
	if len(cs.Attempts) >= MaxAttempts {
		return
	}
	if st == "" {
		st = Failed
	}
	cs.Attempts = append(cs.Attempts, Attempt{Status: st, DurationMs: ms, Message: cleanMessage(msg, c.lim.MaxMessageBytes, false)})
}

// stepIn is a step as a parser read it, before cleaning.
type stepIn struct {
	depth   int
	keyword string
	name    string
	status  Status
	ms      int64
	err     string
	text    string
	textCut bool
	http    *HTTPExchange // already bounded (see parseHTTPLog)
	attach  []int
}

// addStep adds a cleaned step and returns false when the case has no room
// left (MaxSteps) or the step is nested too deep (MaxStepDepth); either
// way the first such cut is noted in the warning.
func (c *collector) addStep(cs *Case, s stepIn) bool {
	if s.depth > MaxStepDepth {
		c.warnOnce("stepdepth", "some steps are nested more than %d deep; the deeper ones were left out", MaxStepDepth)
		return false
	}
	if len(cs.Steps) >= c.lim.MaxSteps {
		c.warnOnce("steps", "some cases have more than %d steps; the rest were left out", c.lim.MaxSteps)
		return false
	}
	st := s.status
	switch st {
	case Passed, Failed, Errored, Skipped:
	default:
		st = Skipped
	}
	step := Step{
		Depth:       max(s.depth, 0),
		Keyword:     cleanName(s.keyword, maxKeywordBytes),
		Name:        cleanName(s.name, c.lim.MaxNameBytes),
		Status:      st,
		DurationMs:  s.ms,
		Error:       cleanMessage(s.err, c.lim.MaxMessageBytes, false),
		Text:        cleanBlock(s.text, c.lim.MaxStepTextBytes, s.textCut),
		HTTP:        s.http,
		Attachments: s.attach,
	}
	cs.Steps = append(cs.Steps, step)
	return true
}

// roomForAttachment says whether the case can take one more attachment,
// noting the first time it cannot.
func (c *collector) roomForAttachment(cs *Case) bool {
	if len(cs.Attachments) < c.lim.MaxAttachments {
		return true
	}
	c.warnOnce("attachments", "some cases have more than %d attachments; the rest were left out", c.lim.MaxAttachments)
	return false
}

// cleanPath cleans an attachment path the way every other string is
// cleaned, and nothing more: it is never resolved here. It returns "" for
// a path that is empty or longer than MaxPathBytes.
func cleanPath(p string) string {
	if len(p) > 4*MaxPathBytes {
		return ""
	}
	t := strings.TrimSpace(cleanText(p))
	if t == "" || len(t) > MaxPathBytes || strings.ContainsAny(t, "\n\t") {
		return ""
	}
	return t
}

// baseName is the last element of a slash or backslash path.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// addAttachmentPath adds an attachment named by path and returns its index,
// or -1 when it was left out (no room, or a bad path).
func (c *collector) addAttachmentPath(cs *Case, name, ctype, path string) int {
	p := cleanPath(path)
	if p == "" {
		if strings.TrimSpace(path) != "" {
			c.warnOnce("path", "some attachment paths are longer than %d bytes or hold line breaks and were left out", MaxPathBytes)
		}
		return -1
	}
	for i, a := range cs.Attachments {
		if a.Path == p {
			return i // the same file attached twice
		}
	}
	if !c.roomForAttachment(cs) {
		return -1
	}
	n := cleanName(name, MaxAttachName)
	if n == "" {
		n = cutHead(baseName(p), MaxAttachName, false)
	}
	cs.Attachments = append(cs.Attachments, Attachment{Name: n, ContentType: cleanName(ctype, maxContentTypeBytes), Path: p})
	return len(cs.Attachments) - 1
}

// inlineKind says how inline attachment data is written.
type inlineKind int

const (
	inlineBase64 inlineKind = iota // base64 (standard or URL alphabet, padded or not)
	inlineRaw                      // the bytes themselves (text)
	inlineMaybe                    // base64 if it decodes to UTF-8 text, else raw text (Cucumber's text/plain)
)

// addAttachmentData adds an attachment carried inline. Its bytes are kept
// only within MaxInlineBytes for one and MaxInlineTotal for the report;
// beyond them, or when the data cannot be decoded, the entry is kept with
// Problem and Size instead. The encoded length is checked before anything
// is decoded.
func (c *collector) addAttachmentData(cs *Case, name, ctype, data string, kind inlineKind) int {
	if !c.roomForAttachment(cs) {
		return -1
	}
	a := Attachment{Name: cleanName(name, MaxAttachName), ContentType: cleanName(ctype, maxContentTypeBytes)}
	if a.Name == "" {
		a.Name = "attachment"
	}
	if kind == inlineMaybe {
		kind = inlineRaw
		if d, ok := decodeBase64(data, c.lim.MaxInlineBytes); ok && utf8.Valid(d) && len(data) >= 4 {
			kind = inlineBase64
		}
	}
	var size int64
	if kind == inlineBase64 {
		size = base64Size(data)
	} else {
		size = int64(len(data))
	}
	a.Size = size
	switch {
	case size > c.lim.MaxInlineBytes:
		a.Problem = fmt.Sprintf("not kept: %d bytes, over the %d bytes kept for one inline attachment", size, c.lim.MaxInlineBytes)
	case size > c.lim.MaxInlineTotal-c.inlineUsed:
		a.Problem = fmt.Sprintf("not kept: the report's inline attachments passed the %d bytes kept for one report", c.lim.MaxInlineTotal)
	default:
		var b []byte
		if kind == inlineBase64 {
			d, ok := decodeBase64(data, c.lim.MaxInlineBytes)
			if !ok {
				a.Problem = "not kept: the data is not valid base64"
				break
			}
			b = d
		} else {
			b = []byte(data)
		}
		a.Data, a.Size = b, int64(len(b))
		c.inlineUsed += a.Size
	}
	if a.Problem != "" {
		c.warnOnce("inline", "some inline attachments were not kept (%s)", strings.TrimPrefix(a.Problem, "not kept: "))
	}
	cs.Attachments = append(cs.Attachments, a)
	return len(cs.Attachments) - 1
}

// base64Size estimates the decoded size of base64 text without decoding it.
func base64Size(s string) int64 {
	n := int64(0)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '=', ' ', '\n', '\r', '\t':
		default:
			n++
		}
	}
	return n * 3 / 4
}

// decodeBase64 decodes standard or URL-alphabet base64, with or without
// padding and line breaks; it refuses text that would decode to more than
// max bytes.
func decodeBase64(s string, max int64) ([]byte, bool) {
	if base64Size(s) > max {
		return nil, false
	}
	if strings.ContainsAny(s, " \n\r\t") {
		s = strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, s)
	}
	s = strings.TrimRight(s, "=")
	enc := base64.RawStdEncoding
	if strings.ContainsAny(s, "-_") {
		enc = base64.RawURLEncoding
	}
	b, err := enc.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// msToMs turns a duration in (possibly fractional) milliseconds into whole
// milliseconds, with the same rules as secondsToMs.
func msToMs(f float64) int64 {
	return secondsToMs(f / 1000)
}

// nsToMs turns nanoseconds into milliseconds.
func nsToMs(f float64) int64 {
	return secondsToMs(f / 1e9)
}

// jsonNumber reads a number that may have been written as a string.
func jsonNumber(s string) float64 {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && errors.Is(err, strconv.ErrRange) && f > 0 {
		return 1e300 // too big for a float: as long as allowed
	}
	if err != nil || math.IsNaN(f) {
		return 0
	}
	return f
}

// HideCredentials returns the value of an HTTP header, or "[hidden]" when
// the header carries credentials: Authorization, Proxy-Authorization,
// Cookie, Set-Cookie, X-Api-Key, X-Auth-Token, and any header whose name
// holds "token", "secret", "password", "api-key", "apikey" or "api_key"
// (case does not matter).
func HideCredentials(name, value string) string {
	if value == "" {
		return ""
	}
	if sensitiveHeader(name) {
		return hidden
	}
	return value
}

func sensitiveHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-auth-token":
		return true
	}
	for _, s := range [...]string{"token", "secret", "password", "passwd", "api-key", "apikey", "api_key", "credential", "session-id", "sessionid"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// sensitiveParam says whether a URL query parameter carries a credential.
func sensitiveParam(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "key", "sig", "signature", "token", "secret", "password", "pass", "passwd", "pwd", "auth", "code":
		return true
	}
	for _, s := range [...]string{"token", "secret", "password", "signature", "apikey", "api_key", "api-key", "credential", "_key", "-key"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// HideURLCredentials replaces the password (or a bare token) in a URL's
// user information and the values of query parameters that carry
// credentials (token, key, secret, password, sig, signature and names
// holding those words) with "[hidden]". Everything else is kept as written.
func HideURLCredentials(u string) string {
	out := u
	// user information: scheme://user:pass@host
	if i := strings.Index(out, "://"); i >= 0 {
		rest := out[i+3:]
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		if at := strings.LastIndexByte(rest[:end], '@'); at >= 0 {
			info := rest[:at]
			repl := hidden
			if c := strings.IndexByte(info, ':'); c >= 0 {
				repl = info[:c] + ":" + hidden
			}
			out = out[:i+3] + repl + rest[at:]
		}
	}
	q := strings.IndexByte(out, '?')
	if q < 0 {
		return out
	}
	frag := ""
	query := out[q+1:]
	if h := strings.IndexByte(query, '#'); h >= 0 {
		query, frag = query[:h], query[h:]
	}
	parts := strings.Split(query, "&")
	changed := false
	for i, p := range parts {
		k, _, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if sensitiveParam(name) {
			parts[i] = k + "=" + hidden
			changed = true
		}
	}
	if !changed {
		return out
	}
	return out[:q+1] + strings.Join(parts, "&") + frag
}
