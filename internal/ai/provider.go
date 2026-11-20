package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider speaks one model API. Adding a provider means writing these five
// methods (about a hundred lines, see anthropic.go and openai.go) and adding
// it to providers below; the Client does the rest: limits, retries, timeouts,
// schema checks, cost, and errors in words.
type Provider interface {
	// Check refuses model settings the provider cannot use. NewRegistry
	// calls it after the common checks and defaults.
	Check(m Model) error

	// NewRequest builds the HTTP request for one try of a call.
	NewRequest(ctx context.Context, p *Prepared) (*http.Request, error)

	// ParseResponse reads a whole (not streamed) answer.
	ParseResponse(body []byte) (Reply, error)

	// ParseStream reads a streamed answer. It returns ErrTooLarge as soon
	// as the text passes maxText bytes, a *ProviderError for an error the
	// stream reports, Malformed for events it cannot read, and
	// ErrStreamEnded when the stream stops before its end marker.
	ParseStream(r io.Reader, maxText int) (Reply, error)

	// ParseError reads the body of an answer with an error status. The
	// Client fills in Status and RetryAfter.
	ParseError(status int, body []byte) ProviderError

	// Downgrade is asked after a call fails with status 400. If the failure
	// means the server does not support something the call asked for, such
	// as a structured-output mode, Downgrade changes p to ask without it and
	// returns true; the call is then tried once more.
	Downgrade(p *Prepared, e *ProviderError) bool
}

// providers are the providers by the name operators write.
var providers = map[string]Provider{
	"anthropic": anthropicProvider{},
	"openai":    openAIProvider{},
}

// Prepared is one call after its request was checked against the model:
// what a Provider turns into HTTP.
type Prepared struct {
	Model       Model
	Key         string // the API key; empty when the model has none
	System      string
	Prompt      string
	MaxTokens   int
	Temperature *float64
	Stream      bool

	// Schema is the schema as sent to the provider (objects closed, the
	// constraints it refuses moved into descriptions), nil without one.
	// Strict reports whether every object lists all of its properties as
	// required. SchemaMode is the openai model's mode, which Downgrade may
	// change.
	Schema     json.RawMessage
	SchemaText string // the schema as written, for instructions in the prompt
	Strict     bool
	SchemaMode string
}

// schemaInstruction is added to the system text when the schema is given in
// the instructions rather than as a request setting.
func (p *Prepared) schemaInstruction() string {
	s := "Reply with only a JSON object, no other text, that matches this JSON schema:\n" + p.SchemaText
	if p.System == "" {
		return s
	}
	return p.System + "\n\n" + s
}

// Reply is what a provider read from an answer.
type Reply struct {
	Text       string
	StopReason string // end_turn, max_tokens, stop_sequence, refusal, tool_use, or the provider's own word
	Model      string // the model id the provider reported
	Usage      Usage

	Refused       bool
	RefusalDetail string // such as "refusal, category cyber"
}

// ProviderError is an error a provider reported, by status or in a stream.
type ProviderError struct {
	Status     int           // HTTP status; for a stream error, the status it stands for
	Type       string        // the provider's error type, such as rate_limit_error
	Message    string        // the provider's message
	RetryAfter time.Duration // from the retry-after header, 0 if none
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, e.Type, e.Message)
}

// Errors a provider returns from ParseStream.
var (
	ErrTooLarge    = errors.New("oversized output")
	ErrStreamEnded = errors.New("the stream ended before the answer was complete")
)

// malformedError is an answer the provider sent that cannot be read.
type malformedError struct{ msg string }

func (e *malformedError) Error() string { return e.msg }

// Malformed reports an answer that cannot be read.
func Malformed(format string, args ...any) error {
	return &malformedError{fmt.Sprintf(format, args...)}
}

// textBuilder collects an answer's text up to a limit.
type textBuilder struct {
	b   strings.Builder
	max int
}

func (t *textBuilder) add(s string) error {
	if t.b.Len()+len(s) > t.max {
		return ErrTooLarge
	}
	t.b.WriteString(s)
	return nil
}

// limitedBody reads at most n bytes and then fails with ErrTooLarge.
type limitedBody struct {
	r io.Reader
	n int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.n <= 0 {
		// Check whether there is more before calling it oversized.
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return l.r.Read(p)
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// maxSSELine bounds one line of a stream.
const maxSSELine = 1 << 20

// readSSE reads server-sent events and calls fn with each event's name and
// data. It stops when fn returns done, at the end of the stream, or on an
// error.
func readSSE(r io.Reader, fn func(event, data string) (done bool, err error)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var event string
	var data []string
	for {
		line, err := readLine(br)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				if len(data) > 0 {
					if done, ferr := fn(event, strings.Join(data, "\n")); ferr != nil || done {
						return ferr
					}
				}
				return ErrStreamEnded
			}
			return err
		}
		switch {
		case line == "":
			if len(data) > 0 || event != "" {
				done, ferr := fn(event, strings.Join(data, "\n"))
				if ferr != nil {
					return ferr
				}
				if done {
					return nil
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
			// A comment, sent to keep the connection open.
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event = value
			case "data":
				data = append(data, value)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				continue // the last line had no newline; the next read ends it
			}
			return err
		}
	}
}

// readLine reads one line without its line ending.
func readLine(br *bufio.Reader) (string, error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := br.ReadLine()
		buf.Write(chunk)
		if buf.Len() > maxSSELine {
			return "", Malformed("a stream line is longer than %d bytes", maxSSELine)
		}
		if err != nil {
			return buf.String(), err
		}
		if !isPrefix {
			return buf.String(), nil
		}
	}
}

// oneLine shortens a provider's message for an error: one line, at most
// 300 characters.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "..."
	}
	return s
}

// unknownError describes an error body that is not the provider's JSON
// error shape, such as a proxy's HTML page.
func unknownError(status int, body []byte) ProviderError {
	msg := strings.TrimSpace(string(body))
	if msg == "" || bytes.HasPrefix(bytes.TrimSpace(body), []byte("<")) {
		msg = http.StatusText(status)
	}
	return ProviderError{Type: "http_error", Message: oneLine(msg)}
}
