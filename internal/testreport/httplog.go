package testreport

import (
	"regexp"
	"strconv"
	"strings"
)

// Karate logs each HTTP call a step makes like this (1.x; 2.x adds the
// method and URL to the status line and a summary at the end):
//
//	00:52:40.490 request:
//	1 > GET http://127.0.0.1:8099/users/1
//	1 > Host: 127.0.0.1:8099
//	<request body lines>
//
//	00:52:40.528 response time in milliseconds: 36
//	1 < 200
//	1 < content-type: application/json
//	<response body lines>
//
// parseHTTPLog reads the first exchange of such a log into an HTTPExchange.
// A step that made several calls keeps only the first one as its HTTP; the
// whole log (with credentials hidden) stays in the step's Text.

var (
	reHTTPRequest  = regexp.MustCompile(`^(\d+) > ([A-Za-z]+) (\S+)\s*$`)
	reHTTPReqHead  = regexp.MustCompile(`^(\d+) > ([^:\s][^:]*):\s?(.*)$`)
	reHTTPStatus   = regexp.MustCompile(`^(\d+) < (\d{3})(?: ([A-Za-z]+) (\S+))?\s*$`)
	reHTTPRespHead = regexp.MustCompile(`^(\d+) < ([^:\s][^:]*):\s?(.*)$`)
	reHTTPTime     = regexp.MustCompile(`response time in milliseconds: ([\d.]+)`)
	reHTTPStamp    = regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d+ `)
	reHTTPSummary  = regexp.MustCompile(`^\d{3} \((\d+) ms\)$`)
	reHTTPLogLine  = regexp.MustCompile(`(?m)^\d+ > [A-Za-z]+ \S`)
)

// looksLikeHTTPLog says whether a text holds a Karate request line.
func looksLikeHTTPLog(s string) bool {
	return reHTTPLogLine.MatchString(s[:min(len(s), 64<<10)])
}

const maxHTTPLogLines = 100000

func parseHTTPLog(s string, lim Limits) *HTTPExchange {
	const (
		none = iota
		reqHead
		reqBody
		respHead
		respBody
	)
	state := none
	var ex HTTPExchange
	var reqB, respB headBuf
	reqB.max, respB.max = 4*MaxHTTPBody, 4*MaxHTTPBody
	lines := 0
	for s != "" && lines < maxHTTPLogLines {
		lines++
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		line = strings.TrimSuffix(line, "\r")
		if len(line) > 1<<20 {
			continue
		}
		if m := reHTTPRequest.FindStringSubmatch(line); m != nil {
			if state != none {
				break // a second exchange: only the first is kept
			}
			ex.Method = cleanName(strings.ToUpper(m[2]), maxMethodBytes)
			ex.URL = cutHead(HideURLCredentials(cleanName(m[3], 4*MaxURLBytes)), MaxURLBytes, false)
			state = reqHead
			continue
		}
		if state == none {
			continue
		}
		if m := reHTTPStatus.FindStringSubmatch(line); m != nil {
			ex.Status, _ = strconv.Atoi(m[2])
			state = respHead
			continue
		}
		if m := reHTTPTime.FindStringSubmatch(line); m != nil {
			ex.DurationMs = msToMs(jsonNumber(m[1]))
			if state == reqHead || state == reqBody {
				state = respHead // the response follows
			}
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), "request:") || reHTTPStamp.MatchString(line) {
			continue
		}
		switch state {
		case reqHead, reqBody:
			if m := reHTTPReqHead.FindStringSubmatch(line); m != nil && state == reqHead {
				ex.RequestHeaders = addHeader(ex.RequestHeaders, m[2], m[3])
				continue
			}
			if state == reqHead && strings.TrimSpace(line) == "" {
				continue
			}
			state = reqBody
			reqB.writeString(line + "\n")
		case respHead, respBody:
			if m := reHTTPRespHead.FindStringSubmatch(line); m != nil && state == respHead {
				ex.ResponseHeaders = addHeader(ex.ResponseHeaders, m[2], m[3])
				continue
			}
			if state == respHead && strings.TrimSpace(line) == "" {
				continue
			}
			state = respBody
			respB.writeString(line + "\n")
		}
	}
	if ex.Method == "" {
		return nil
	}
	ex.RequestBody = cleanBlock(string(reqB.b), MaxHTTPBody, reqB.over)
	resp := string(respB.b)
	// Karate 2 ends the log with "POST http://...\n201 (6 ms)".
	if t := strings.TrimRight(resp, "\n "); t != "" {
		ls := strings.Split(t, "\n")
		if n := len(ls); n >= 2 {
			if m := reHTTPSummary.FindStringSubmatch(strings.TrimSpace(ls[n-1])); m != nil && strings.HasPrefix(ls[n-2], ex.Method+" ") {
				if ex.DurationMs == 0 {
					ex.DurationMs = msToMs(jsonNumber(m[1]))
				}
				resp = strings.Join(ls[:n-2], "\n")
			}
		}
	}
	ex.ResponseBody = cleanBlock(resp, MaxHTTPBody, respB.over)
	return &ex
}

// addHeader adds a header with credentials hidden, within MaxHeaders and
// MaxHeaderBytes.
func addHeader(hs []Header, name, value string) []Header {
	if len(hs) >= MaxHeaders {
		return hs
	}
	n := cleanName(name, MaxHeaderBytes/2)
	if n == "" {
		return hs
	}
	v := HideCredentials(n, cleanName(value, 4*MaxHeaderBytes))
	v = cutHead(v, MaxHeaderBytes-len(n), false)
	return append(hs, Header{Name: n, Value: v})
}

// hideHTTPLogCredentials hides the credentials in a Karate HTTP log: the
// values of credential headers, and credentials in URLs.
func hideHTTPLogCredentials(s string) string {
	if !strings.Contains(s, " > ") && !strings.Contains(s, " < ") && !strings.Contains(s, "://") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for s != "" {
		line := s
		nl := ""
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s, nl = s[:i], s[i+1:], "\n"
		} else {
			s = ""
		}
		b.WriteString(hideLine(line))
		b.WriteString(nl)
	}
	return b.String()
}

func hideLine(line string) string {
	for _, re := range []*regexp.Regexp{reHTTPReqHead, reHTTPRespHead} {
		if len(line) < 1<<16 {
			if m := re.FindStringSubmatchIndex(line); m != nil {
				name := line[m[4]:m[5]]
				if sensitiveHeader(name) && m[7] > m[6] {
					return line[:m[6]] + hidden
				}
			}
		}
	}
	if !strings.Contains(line, "://") {
		return line
	}
	fields := strings.Split(line, " ")
	for i, f := range fields {
		if strings.Contains(f, "://") {
			fields[i] = HideURLCredentials(f)
		}
	}
	return strings.Join(fields, " ")
}
