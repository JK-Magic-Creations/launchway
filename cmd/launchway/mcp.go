package main

// launchway mcp: the MCP endpoint of a Launchway server, over standard input
// and output, for agents that start their MCP servers as commands. It
// forwards each message to the server's /mcp with the stored token (or
// LAUNCHWAY_TOKEN), so the tools, their rights and their limits are the
// server's; nothing runs locally.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const mcpUsage = `Usage: launchway mcp [--server URL]

Serves the Launchway server's MCP tools on standard input and output, for
agents that start MCP servers as commands. It uses the server and token that
launchway login stored, or LAUNCHWAY_SERVER and LAUNCHWAY_TOKEN (a server
without sign-in needs no token). Nothing runs on this machine: every message
goes to the server's /mcp endpoint, which decides what the token may do.
`

const (
	mcpMaxLine     = 4 << 20 // one message from the client
	mcpMaxInFlight = 8
	mcpCallLimit   = 15 * time.Minute // one request, wait_for_run included
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, mcpUsage) }
	server := fs.String("server", "", "the Launchway server (default: what login stored, or LAUNCHWAY_SERVER)")
	if err := fs.Parse(args); err != nil {
		return exitErr(exitUsage)
	}
	if *server != "" {
		os.Setenv("LAUNCHWAY_SERVER", *server)
	}
	return runClient(func() error {
		c, err := loadCredentials()
		if err != nil && c.Server != "" && c.Token == "" {
			// A server without sign-in (on this machine) takes no token.
			c.Server, err = strings.TrimRight(c.Server, "/"), nil
		}
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		p := &mcpProxy{endpoint: c.Server + "/mcp", token: c.Token, out: os.Stdout, log: os.Stderr,
			http: &http.Client{Transport: http.DefaultTransport}}
		return p.serve(ctx, os.Stdin)
	})
}

// mcpProxy forwards newline-delimited JSON-RPC messages to an MCP endpoint
// over Streamable HTTP and writes the answers back, one per line.
type mcpProxy struct {
	endpoint, token string
	out             io.Writer
	log             io.Writer
	http            *http.Client

	outMu    sync.Mutex
	mu       sync.Mutex
	inFlight map[string]context.CancelFunc // by request id
	legacy   string                        // the version an initialize agreed, for older clients
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func (p *mcpProxy) serve(ctx context.Context, in io.Reader) error {
	p.inFlight = map[string]context.CancelFunc{}
	rd := bufio.NewReaderSize(in, 64<<10)
	slots := make(chan struct{}, mcpMaxInFlight)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		line, err := readLine(rd, mcpMaxLine)
		if errors.Is(err, errLineTooLong) {
			p.write(rpcError(nil, -32700, fmt.Sprintf("the message is longer than %d MiB", mcpMaxLine>>20)))
			continue
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var m rpcMessage
			if jerr := json.Unmarshal(line, &m); jerr != nil {
				p.write(rpcError(nil, -32700, "not a JSON-RPC message: "+jerr.Error()))
			} else if m.Method == "notifications/cancelled" {
				p.cancel(m.Params)
			} else if m.Method != "" {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return nil
				}
				wg.Add(1)
				go func(raw []byte, m rpcMessage) {
					defer wg.Done()
					defer func() { <-slots }()
					p.forward(ctx, raw, m)
				}(append([]byte(nil), line...), m)
			}
			// A response from the client: the server never asks it anything
			// over this transport, so there is nothing to forward it to.
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

var errLineTooLong = errors.New("line too long")

// readLine reads one line of at most max bytes; a longer one is read to its
// end and dropped.
func readLine(rd *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := rd.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if tooLong && err == nil {
			return nil, errLineTooLong
		}
		return buf, err
	}
}

func (p *mcpProxy) cancel(params json.RawMessage) {
	var c struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(params, &c) == nil {
		p.mu.Lock()
		if f := p.inFlight[string(c.RequestID)]; f != nil {
			f() // closing the request's stream is how HTTP cancels it
		}
		p.mu.Unlock()
	}
}

// forward sends one message and writes back what the server answers.
func (p *mcpProxy) forward(ctx context.Context, raw []byte, m rpcMessage) {
	ctx, cancel := context.WithTimeout(ctx, mcpCallLimit)
	defer cancel()
	if m.ID != nil {
		p.mu.Lock()
		p.inFlight[string(m.ID)] = cancel
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			delete(p.inFlight, string(m.ID))
			p.mu.Unlock()
		}()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(raw))
	if err != nil {
		p.fail(m, err.Error())
		return
	}
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	if p.token != "" {
		h.Set("Authorization", "Bearer "+p.token)
	}
	h.Set("User-Agent", "launchway-mcp/"+version)
	var params struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
		Meta struct {
			Version string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(m.Params, &params)
	switch {
	case params.Meta.Version != "":
		h.Set("MCP-Protocol-Version", params.Meta.Version)
	case m.Method != "initialize":
		p.mu.Lock()
		if p.legacy != "" {
			h.Set("MCP-Protocol-Version", p.legacy)
		}
		p.mu.Unlock()
	}
	h.Set("Mcp-Method", m.Method)
	switch m.Method {
	case "tools/call", "prompts/get":
		h.Set("Mcp-Name", headerValue(params.Name))
	case "resources/read":
		h.Set("Mcp-Name", headerValue(params.URI))
	}
	resp, err := p.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // canceled: nothing more is sent for it
		}
		p.fail(m, "the Launchway server could not be reached: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		p.relayEvents(resp.Body, m)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if p.relay(body, m) {
		return
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		msg = "the token was refused (" + msg + "); run launchway login, or set LAUNCHWAY_TOKEN"
	default:
		msg = fmt.Sprintf("the Launchway server answered %s: %s", resp.Status, msg)
	}
	p.fail(m, msg)
}

// relayEvents writes each message of an SSE response as a line.
func (p *mcpProxy) relayEvents(body io.Reader, m rpcMessage) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	var data []byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if len(data) > 0 {
				p.relay(data, m)
				data = nil
			}
		case bytes.HasPrefix(line, []byte("data:")):
			d := bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, d...)
		}
	}
	if len(data) > 0 {
		p.relay(data, m)
	}
}

// relay writes a JSON-RPC message from the server as one line, and reports
// whether it was one. An error the server sent without an id answers the
// request it was sent for.
func (p *mcpProxy) relay(data []byte, m rpcMessage) bool {
	var msg map[string]json.RawMessage
	if json.Unmarshal(data, &msg) != nil || string(msg["jsonrpc"]) != `"2.0"` {
		return false
	}
	if _, ok := msg["error"]; ok && m.ID != nil && (msg["id"] == nil || string(msg["id"]) == "null") {
		msg["id"] = m.ID
	}
	if m.Method == "initialize" {
		var res struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(msg["result"], &res) == nil && res.ProtocolVersion != "" {
			p.mu.Lock()
			p.legacy = res.ProtocolVersion
			p.mu.Unlock()
		}
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	p.write(out)
	return true
}

func (p *mcpProxy) fail(m rpcMessage, msg string) {
	if m.ID == nil {
		fmt.Fprintln(p.log, "launchway mcp:", msg)
		return
	}
	p.write(rpcError(m.ID, -32000, msg))
}

func (p *mcpProxy) write(line []byte) {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.out.Write(append(line, '\n'))
}

func rpcError(id json.RawMessage, code int, msg string) []byte {
	if id == nil {
		id = json.RawMessage("null")
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	return out
}

// headerValue writes a value for an Mcp-Name header: as it is if it is
// plain printable ASCII, else in the transport's base64 form.
func headerValue(v string) string {
	plain := v == strings.TrimSpace(v)
	for i := 0; i < len(v) && plain; i++ {
		plain = v[i] >= 0x20 && v[i] < 0x7f
	}
	if plain {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}
