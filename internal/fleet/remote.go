package fleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Result is the outcome of one remote command.
type Result struct {
	Stdout, Stderr string
	Exit           int
}

// Remote runs commands on hosts. Implementations must refuse a host whose
// key is not the pinned one.
type Remote interface {
	Run(ctx context.Context, h Host, cmd string, stdin io.Reader) (Result, error)
}

// AuditEntry is one command, written before it runs and again when it ends.
type AuditEntry struct {
	Time     time.Time     `json:"time"`
	Run      string        `json:"run,omitempty"`
	Host     string        `json:"host"`
	Command  string        `json:"command"`
	StdinLen int64         `json:"stdin_bytes,omitempty"`
	StdinSHA string        `json:"stdin_sha256,omitempty"`
	Exit     int           `json:"exit"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Done     bool          `json:"done"` // false: about to run; true: finished
}

// Audited wraps a Remote so that every command is recorded: its text, the
// size and hash of what was sent on stdin (never the content), its exit
// status and its duration. Commands never contain secrets: they take them
// from stdin.
type Audited struct {
	Remote Remote
	RunID  string
	Sink   func(AuditEntry)
	mu     sync.Mutex
}

// Run runs the command, recording it.
func (a *Audited) Run(ctx context.Context, h Host, cmd string, stdin io.Reader) (Result, error) {
	e := AuditEntry{Time: time.Now(), Run: a.RunID, Host: h.Name, Command: cmd}
	if stdin != nil {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return Result{}, err
		}
		sum := sha256.Sum256(data)
		e.StdinLen, e.StdinSHA = int64(len(data)), hex.EncodeToString(sum[:])
		stdin = bytes.NewReader(data)
	}
	a.record(e)
	res, err := a.Remote.Run(ctx, h, cmd, stdin)
	e.Done, e.Exit, e.Duration = true, res.Exit, time.Since(e.Time)
	if err != nil {
		e.Error = err.Error()
	}
	a.record(e)
	return res, err
}

func (a *Audited) record(e AuditEntry) {
	if a.Sink == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Sink(e)
}

// SSH is the Remote that uses the SSH protocol. The Station's key comes from
// the secret store by reference and is held here only in memory.
type SSH struct {
	Signer  ssh.Signer
	Timeout time.Duration // dial timeout; default 10s
}

// NewSSH reads a PEM private key.
func NewSSH(pemKey []byte) (*SSH, error) {
	s, err := ssh.ParsePrivateKey(pemKey)
	if err != nil {
		return nil, fmt.Errorf("the deploy key could not be read: %w", err)
	}
	return &SSH{Signer: s}, nil
}

// ErrHostKey says a host presented a key other than the pinned one.
var ErrHostKey = errors.New("host key does not match the pinned key")

// Run implements Remote.
func (s *SSH) Run(ctx context.Context, h Host, cmd string, stdin io.Reader) (Result, error) {
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
	if err != nil {
		return Result{}, fmt.Errorf("host %s: the pinned host key cannot be read: %w", h.Name, err)
	}
	user := h.User
	if user == "" {
		user = "deploy"
	}
	addr := h.Address
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "22")
	}
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(s.Signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
				return fmt.Errorf("%w: host %s presented %s", ErrHostKey, h.Name, ssh.FingerprintSHA256(key))
			}
			return nil
		},
		HostKeyAlgorithms: []string{pinned.Type()},
		Timeout:           orDur(s.Timeout, 10*time.Second),
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{}, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return Result{}, err
	}
	cl := ssh.NewClient(c, chans, reqs)
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	var out, errb limited
	sess.Stdout, sess.Stderr, sess.Stdin = &out, &errb, stdin
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		sess.Close()
		return Result{}, ctx.Err()
	case err := <-done:
		res := Result{Stdout: out.String(), Stderr: errb.String()}
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			res.Exit = ee.ExitStatus()
			return res, nil
		}
		return res, err
	}
}

func orDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// limited keeps the first 64 KiB of a command's output.
type limited struct{ b bytes.Buffer }

func (l *limited) Write(p []byte) (int, error) {
	if room := 64<<10 - l.b.Len(); room > 0 {
		l.b.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
func (l *limited) String() string { return strings.TrimSpace(l.b.String()) }
