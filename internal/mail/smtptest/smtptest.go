// Package smtptest is an SMTP server in a test's own process, for testing
// code that sends mail. It is strict where real servers are: with RequireTLS
// (the default) it advertises no AUTH and accepts no MAIL until the
// connection is encrypted, and it checks the credentials it was given. It
// never sends anything anywhere.
package smtptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Received is one message the server accepted.
type Received struct {
	From, To string
	Data     string
	TLS      bool
	User     string
}

// Server is the fake.
type Server struct {
	Addr       string // host:port to connect to
	Host       string
	Port       int
	RequireTLS bool   // advertise AUTH and accept MAIL only on an encrypted connection (default true)
	Implicit   bool   // TLS from the first byte (port 465 style)
	NoSTARTTLS bool   // do not offer STARTTLS at all
	User, Pass string // when User is set, the server insists on AUTH PLAIN with them

	mu        sync.Mutex
	got       []Received
	fail      int // refuse this many deliveries with a temporary error
	conns     int
	ln        net.Listener
	cert      tls.Certificate
	pool      *x509.CertPool
	plainMAIL int // MAIL commands seen before encryption
}

// New starts a server on a free local port; Close stops it.
func New() (*Server, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtptest"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{RequireTLS: true, ln: ln, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool: pool}
	s.Addr = ln.Addr().String()
	s.Host, _, _ = net.SplitHostPort(s.Addr)
	fmt.Sscan(strings.TrimPrefix(s.Addr, s.Host+":"), &s.Port)
	go s.serve()
	return s, nil
}

// ClientTLS is the TLS configuration a client needs to trust this server.
func (s *Server) ClientTLS() *tls.Config { return &tls.Config{RootCAs: s.pool} }

// Close stops the server.
func (s *Server) Close() { _ = s.ln.Close() }

// FailNext makes the next n deliveries end with a temporary error.
func (s *Server) FailNext(n int) { s.mu.Lock(); s.fail = n; s.mu.Unlock() }

// Messages returns what was accepted.
func (s *Server) Messages() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.got...)
}

// PlainMail is how many times a client tried to send mail before encrypting.
func (s *Server) PlainMail() int { s.mu.Lock(); defer s.mu.Unlock(); return s.plainMAIL }

// Connections is how many connections were made.
func (s *Server) Connections() int { s.mu.Lock(); defer s.mu.Unlock(); return s.conns }

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	secure := false
	if s.Implicit {
		c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
		secure = true
	}
	r := bufio.NewReader(c)
	say := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
	say("220 smtptest ready")
	var from, to, user string
	authed := s.User == ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			say("250-smtptest")
			if !secure && !s.NoSTARTTLS {
				say("250-STARTTLS")
			}
			if s.User != "" && (secure || !s.RequireTLS) {
				say("250-AUTH PLAIN")
			}
			say("250 8BITMIME")
		case "STARTTLS":
			if secure || s.NoSTARTTLS {
				say("503 not now")
				continue
			}
			say("220 go ahead")
			c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
			r = bufio.NewReader(c)
			secure = true
			from, to = "", ""
		case "AUTH":
			if !secure && s.RequireTLS {
				say("538 encryption required for authentication")
				continue
			}
			mech, resp, _ := strings.Cut(arg, " ")
			raw, err := base64.StdEncoding.DecodeString(resp)
			f := strings.Split(string(raw), "\x00")
			if !strings.EqualFold(mech, "PLAIN") || err != nil || len(f) != 3 || f[1] != s.User || f[2] != s.Pass {
				say("535 authentication failed")
				continue
			}
			authed, user = true, f[1]
			say("235 ok")
		case "MAIL":
			if !secure {
				s.mu.Lock()
				s.plainMAIL++
				s.mu.Unlock()
				if s.RequireTLS {
					say("530 must issue STARTTLS first")
					continue
				}
			}
			if !authed {
				say("530 authentication required")
				continue
			}
			from = addrOf(arg)
			say("250 ok")
		case "RCPT":
			to = addrOf(arg)
			say("250 ok")
		case "DATA":
			if from == "" || to == "" {
				say("503 need MAIL and RCPT first")
				continue
			}
			say("354 end with <CRLF>.<CRLF>")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			if s.fail > 0 {
				s.fail--
				s.mu.Unlock()
				say("451 try again later")
				continue
			}
			s.got = append(s.got, Received{From: from, To: to, Data: b.String(), TLS: secure, User: user})
			s.mu.Unlock()
			say("250 queued")
			from, to = "", ""
		case "RSET":
			from, to = "", ""
			say("250 ok")
		case "NOOP":
			say("250 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

// addrOf reads the address out of "FROM:<a@b> BODY=8BITMIME" or "TO:<a@b>".
func addrOf(arg string) string {
	i, j := strings.IndexByte(arg, '<'), strings.IndexByte(arg, '>')
	if i < 0 || j < i {
		return ""
	}
	return arg[i+1 : j]
}
