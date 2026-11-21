package gitssh

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Services a session may run.
const (
	UploadPack    = "git-upload-pack"
	ReceivePack   = "git-receive-pack"
	UploadArchive = "git-upload-archive"
)

// Identity is who an authenticated connection is. Name is shown in
// greetings and logs; Data is the Handler's own (it comes back with every
// request).
type Identity struct {
	Name        string
	Fingerprint string
	Data        any
}

// Command is what an exec request asked for.
type Command struct {
	Service string // UploadPack, ReceivePack or UploadArchive
	Path    string // the repository path as sent, quotes and leading slash removed
	// Protocol is the Git protocol version string the client sent in
	// GIT_PROTOCOL (for example "version=2"), or "".
	Protocol string
}

// Session is the channel of one command.
type Session struct {
	Identity Identity
	Remote   string
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
}

// Handler is what the server asks of the application.
type Handler interface {
	// Authenticate looks a public key up. A refusal is any error; its text is
	// logged, never sent (the client learns only that the key was refused).
	Authenticate(ctx context.Context, key ssh.PublicKey, remote string) (Identity, error)
	// AuthenticatePrincipals maps the principals of a certificate from a
	// trusted authority to an identity.
	AuthenticatePrincipals(ctx context.Context, principals []string, cert *ssh.Certificate, remote string) (Identity, error)
	// Used is called once a connection has authenticated, so that a key's last
	// use is recorded only for a signature that was checked.
	Used(ctx context.Context, id Identity, remote string)
	// Serve runs one command and returns its exit status. It writes what the
	// person should read to s.Err.
	Serve(ctx context.Context, s Session, c Command) int
	// Greeting is what a shell request (a bare "ssh -T") is answered with.
	Greeting(id Identity) string
}

// Config configures a Server. Zero values are the defaults noted.
type Config struct {
	HostKeys []ssh.Signer
	// UserCAs are the public keys of authorities whose user certificates are accepted.
	UserCAs []ssh.PublicKey
	Handler Handler
	Log     *slog.Logger
	// MaxConns caps connections in all (256), MaxPerAddr those from one
	// address (20).
	MaxConns, MaxPerAddr int
	// LoginGrace is the time to complete the handshake and authenticate (20s).
	LoginGrace time.Duration
	// MaxAuthTries is how many keys a client may try on one connection (6).
	MaxAuthTries int
	// FailLimit failed authentications from one address within FailWindow
	// (10 in a minute) close its connections for BanFor (5m).
	FailLimit          int
	FailWindow, BanFor time.Duration
	// IdleTimeout ends a connection that moves no bytes (10m); MaxDuration
	// ends any connection (6h).
	IdleTimeout, MaxDuration time.Duration
	// Now is the clock; for tests.
	Now func() time.Time
}

// Server is the SSH server.
type Server struct {
	c    Config
	sc   *ssh.ServerConfig
	mu   sync.Mutex
	conn map[string]int
	n    int
	fail map[string]*failures
	ids  sync.Map // session ID -> Identity
	ln   net.Listener
	wg   sync.WaitGroup
	done chan struct{}
}

type failures struct {
	times []time.Time
	until time.Time
}

// Algorithms: modern only. No SHA-1 key exchange, no CBC, no ssh-rsa with
// SHA-1 signatures.
var (
	keyExchanges = []string{ssh.KeyExchangeMLKEM768X25519, ssh.KeyExchangeCurve25519, "curve25519-sha256@libssh.org", ssh.KeyExchangeECDHP256, ssh.KeyExchangeECDHP384, ssh.KeyExchangeECDHP521}
	ciphers      = []string{ssh.CipherChaCha20Poly1305, ssh.CipherAES256GCM, ssh.CipherAES128GCM, ssh.CipherAES256CTR, ssh.CipherAES192CTR, ssh.CipherAES128CTR}
	macs         = []string{ssh.HMACSHA256ETM, ssh.HMACSHA512ETM}
	authAlgs     = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256,
		ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512,
		ssh.CertAlgoED25519v01, ssh.CertAlgoECDSA256v01, ssh.CertAlgoECDSA384v01, ssh.CertAlgoECDSA521v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01}
)

// New makes a Server.
func New(c Config) (*Server, error) {
	if c.Handler == nil || len(c.HostKeys) == 0 {
		return nil, errors.New("gitssh: a Handler and at least one host key are required")
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	def := func(p *int, v int) {
		if *p <= 0 {
			*p = v
		}
	}
	defd := func(p *time.Duration, v time.Duration) {
		if *p <= 0 {
			*p = v
		}
	}
	def(&c.MaxConns, 256)
	def(&c.MaxPerAddr, 20)
	def(&c.MaxAuthTries, 6)
	def(&c.FailLimit, 10)
	defd(&c.LoginGrace, 20*time.Second)
	defd(&c.FailWindow, time.Minute)
	defd(&c.BanFor, 5*time.Minute)
	defd(&c.IdleTimeout, 10*time.Minute)
	defd(&c.MaxDuration, 6*time.Hour)
	if c.Now == nil {
		c.Now = time.Now
	}
	s := &Server{c: c, conn: map[string]int{}, fail: map[string]*failures{}, done: make(chan struct{})}
	s.sc = &ssh.ServerConfig{
		Config:                      ssh.Config{KeyExchanges: keyExchanges, Ciphers: ciphers, MACs: macs},
		PublicKeyAuthAlgorithms:     authAlgs,
		MaxAuthTries:                c.MaxAuthTries,
		PublicKeyCallback:           s.publicKey,
		ServerVersion:               "SSH-2.0-Launchway",
		NoClientAuth:                false,
		PasswordCallback:            nil,
		KeyboardInteractiveCallback: nil,
	}
	for _, k := range c.HostKeys {
		s.sc.AddHostKey(k)
	}
	return s, nil
}

// Serve accepts connections on ln until Close or until ctx ends; it returns
// nil after either. Each connection's context is a child of ctx.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.done:
		}
	}()
	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, nc)
		}()
	}
}

// Addr is the address the server listens on.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Close stops listening and closes every connection's session by letting
// the context end; it waits for them.
func (s *Server) Close() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
	}
	s.mu.Lock()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func addrOf(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

// admit applies the connection caps and the ban on addresses that keep
// failing; the returned function releases the slot.
func (s *Server) admit(addr string) (release func(), why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.c.Now()
	if f := s.fail[addr]; f != nil && now.Before(f.until) {
		return nil, "too many failed sign-ins from this address; try again later"
	}
	if s.n >= s.c.MaxConns {
		return nil, "this server has too many SSH connections; try again in a minute"
	}
	if s.conn[addr] >= s.c.MaxPerAddr {
		return nil, "too many connections from this address"
	}
	s.n++
	s.conn[addr]++
	return func() {
		s.mu.Lock()
		s.n--
		if s.conn[addr]--; s.conn[addr] <= 0 {
			delete(s.conn, addr)
		}
		s.mu.Unlock()
	}, ""
}

// failed counts a refused authentication; the address is banned at the limit.
func (s *Server) failed(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.c.Now()
	f := s.fail[addr]
	if f == nil {
		f = &failures{}
		s.fail[addr] = f
	}
	keep := f.times[:0]
	for _, t := range f.times {
		if now.Sub(t) < s.c.FailWindow {
			keep = append(keep, t)
		}
	}
	f.times = append(keep, now)
	if len(f.times) >= s.c.FailLimit {
		f.until, f.times = now.Add(s.c.BanFor), nil
	}
	if len(s.fail) > 4096 { // forget quiet addresses
		for a, g := range s.fail {
			if len(g.times) == 0 && now.After(g.until) {
				delete(s.fail, a)
			}
		}
	}
}

func (s *Server) publicKey(cm ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	remote := addrOf(cm.RemoteAddr())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var id Identity
	var err error
	if cert, ok := key.(*ssh.Certificate); ok {
		id, err = s.certificate(ctx, cert, remote)
	} else {
		id, err = s.c.Handler.Authenticate(ctx, key, remote)
		id.Fingerprint = ssh.FingerprintSHA256(key)
	}
	if err != nil {
		s.c.Log.Info("ssh: key refused", "remote", remote, "fingerprint", ssh.FingerprintSHA256(key), "why", err.Error())
		return nil, errors.New("refused")
	}
	s.ids.Store(string(cm.SessionID()), id)
	return &ssh.Permissions{}, nil
}

func (s *Server) certificate(ctx context.Context, cert *ssh.Certificate, remote string) (Identity, error) {
	if len(s.c.UserCAs) == 0 {
		return Identity{}, errors.New("certificates are not accepted here")
	}
	if cert.CertType != ssh.UserCert || len(cert.ValidPrincipals) == 0 {
		return Identity{}, errors.New("not a user certificate with principals")
	}
	if len(cert.CriticalOptions) > 0 {
		return Identity{}, errors.New("certificates with critical options are not accepted")
	}
	chk := &ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool {
			for _, ca := range s.c.UserCAs {
				if bytes.Equal(ca.Marshal(), auth.Marshal()) {
					return true
				}
			}
			return false
		},
		Clock: s.c.Now,
	}
	// CheckCert judges validity (signature, dates, principals); whether the signer is one
	// we trust is the caller's to decide, and it is decided here.
	if !chk.IsUserAuthority(cert.SignatureKey) {
		return Identity{}, errors.New("the certificate is signed by an authority this server does not trust")
	}
	var verr error
	for _, p := range cert.ValidPrincipals {
		if verr = chk.CheckCert(p, cert); verr == nil {
			break
		}
	}
	if verr != nil {
		return Identity{}, verr
	}
	id, err := s.c.Handler.AuthenticatePrincipals(ctx, cert.ValidPrincipals, cert, remote)
	id.Fingerprint = ssh.FingerprintSHA256(cert.Key) + " (certificate " + fmt.Sprint(cert.Serial) + ")"
	return id, err
}

// deadlineConn moves the deadline with every transfer, so only a connection
// that has gone quiet times out.
type deadlineConn struct {
	net.Conn
	mu   sync.Mutex
	idle time.Duration // 0 until the handshake is over
}

func (d *deadlineConn) arm() {
	d.mu.Lock()
	idle := d.idle
	d.mu.Unlock()
	if idle > 0 {
		_ = d.SetDeadline(time.Now().Add(idle))
	}
}
func (d *deadlineConn) Read(p []byte) (int, error)  { d.arm(); return d.Conn.Read(p) }
func (d *deadlineConn) Write(p []byte) (int, error) { d.arm(); return d.Conn.Write(p) }

func (s *Server) handle(parent context.Context, nc net.Conn) {
	addr := addrOf(nc.RemoteAddr())
	release, why := s.admit(addr)
	if release == nil {
		_, _ = io.WriteString(nc, "SSH-2.0-Launchway\r\n"+why+"\r\n")
		nc.Close()
		return
	}
	defer release()
	dc := &deadlineConn{Conn: nc}
	defer dc.Close()
	_ = nc.SetDeadline(time.Now().Add(s.c.LoginGrace))
	sconn, chans, reqs, err := ssh.NewServerConn(dc, s.sc)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.failed(addr)
			s.c.Log.Debug("ssh: handshake failed", "remote", addr, "err", err)
		}
		return
	}
	defer s.ids.Delete(string(sconn.SessionID()))
	v, _ := s.ids.Load(string(sconn.SessionID()))
	id, _ := v.(Identity)
	dc.mu.Lock()
	dc.idle = s.c.IdleTimeout
	dc.mu.Unlock()
	_ = nc.SetDeadline(time.Now().Add(s.c.IdleTimeout))
	life := time.AfterFunc(s.c.MaxDuration, func() { _ = sconn.Close() })
	defer life.Stop()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-s.done:
			_ = sconn.Close()
		case <-ctx.Done():
		}
	}()
	s.c.Handler.Used(ctx, id, addr)
	go ssh.DiscardRequests(reqs) // no global requests: no port forwarding
	var wg sync.WaitGroup
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.Prohibited, "only Git sessions are allowed")
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.session(ctx, ch, creqs, id, addr)
		}()
	}
	cancel()
	wg.Wait()
}

type execPayload struct{ Command string }
type envPayload struct{ Name, Value string }

func (s *Server) session(ctx context.Context, ch ssh.Channel, reqs <-chan *ssh.Request, id Identity, remote string) {
	defer ch.Close()
	proto := ""
	for req := range reqs {
		switch req.Type {
		case "env":
			var e envPayload
			if ssh.Unmarshal(req.Payload, &e) == nil && e.Name == "GIT_PROTOCOL" && len(e.Value) <= 64 && protoOK.MatchString(e.Value) {
				proto = e.Value
				_ = req.Reply(true, nil)
			} else {
				_ = req.Reply(false, nil)
			}
		case "exec":
			var e execPayload
			if ssh.Unmarshal(req.Payload, &e) != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			cmd, err := ParseCommand(e.Command)
			if err != nil {
				fmt.Fprintf(ch.Stderr(), "Launchway: %v\n", err)
				exit(ch, 1)
				return
			}
			cmd.Protocol = proto
			status := s.c.Handler.Serve(ctx, Session{Identity: id, Remote: remote, In: ch, Out: ch, Err: ch.Stderr()}, cmd)
			_ = ch.CloseWrite()
			exit(ch, status)
			return
		case "shell":
			_ = req.Reply(true, nil)
			_, _ = io.WriteString(ch, strings.ReplaceAll(s.c.Handler.Greeting(id), "\n", "\r\n")+"\r\n")
			exit(ch, 0)
			return
		case "window-change", "signal":
			// harmless; no reply is wanted
		default:
			// pty-req, x11-req, auth-agent-req@openssh.com, subsystem and the rest
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

var protoOK = regexp.MustCompile(`^[0-9a-zA-Z]+=[0-9a-zA-Z]+(:[0-9a-zA-Z]+=[0-9a-zA-Z]+)*$`)

func exit(ch ssh.Channel, status int) {
	b := make([]byte, 4)
	status = max(0, min(status, 255))
	binary.BigEndian.PutUint32(b, uint32(status)) //nolint:gosec // clamped to 0..255 above
	_, _ = ch.SendRequest("exit-status", false, b)
}

// repoPath is owner/name, with an optional .git, one slash, nothing else.
var repoPath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ParseCommand reads an exec request: one of the three Git services and a
// repository path, with optional single or double quotes around the path.
// Anything else is refused with a one-line message in plain words.
func ParseCommand(line string) (Command, error) {
	line = strings.TrimSpace(line)
	svc, rest, _ := strings.Cut(line, " ")
	switch svc {
	case UploadPack, ReceivePack, UploadArchive:
	default:
		name, _, _ := strings.Cut(line, " ")
		if len(name) > 40 {
			name = name[:40] + "..."
		}
		return Command{}, fmt.Errorf("%q is not available: this server runs only git-upload-pack, git-receive-pack and git-upload-archive; shell access is not provided", strings.TrimSpace(name))
	}
	arg := strings.TrimSpace(rest)
	if len(arg) >= 2 && (arg[0] == '\'' && arg[len(arg)-1] == '\'' || arg[0] == '"' && arg[len(arg)-1] == '"') {
		arg = arg[1 : len(arg)-1]
	}
	arg = strings.TrimSuffix(strings.TrimPrefix(arg, "/"), "/")
	p := strings.TrimSuffix(arg, ".git")
	if !repoPath.MatchString(p) {
		return Command{}, errors.New("the repository path must be owner/name, for example acme/shop.git")
	}
	return Command{Service: svc, Path: p}, nil
}
