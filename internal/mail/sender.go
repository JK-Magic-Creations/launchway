package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Security says how the connection to the mail server is protected.
const (
	StartTLS = "starttls" // plain connection, upgraded before anything else (port 587); the default
	TLS      = "tls"      // TLS from the first byte (port 465)
	None     = "none"     // no encryption: only with Config.AllowPlain
)

// Config is the sender's settings (the mail section of the installation's
// configuration).
type Config struct {
	Host     string
	Port     int
	Security string
	// Username and Password are references to a secret store; an empty
	// Username sends without signing in (a relay that trusts the network).
	Username *pipeline.SecretRef
	Password *pipeline.SecretRef
	// From is the sender address, "Launchway <notify@example.com>".
	From string
	// AllowPlain lets Security none and a server that does not offer
	// encryption be used: for a relay on a network the installation trusts.
	AllowPlain bool
	// RootCAs, when set, is who the server's certificate is checked against
	// (tests; empty uses the system's).
	TLSConfig *tls.Config
	// Timeout bounds one send (default 30s).
	Timeout time.Duration
}

// Secrets looks up a secret reference.
type Secrets interface {
	Lookup(ctx context.Context, project string, ref pipeline.SecretRef) (string, error)
}

// Dialer is how the sender connects: the egress guard's.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Message is one message to one person.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// Headers are added after the standard ones (List-Unsubscribe).
	Headers map[string]string
	ID      string // the Message-ID's local part
}

// Enabled says whether a server is configured.
func (c Config) Enabled() bool { return c.Host != "" }

// Check reports what is wrong with the settings.
func (c Config) Check() error {
	if c.Host == "" {
		return nil
	}
	if strings.ContainsAny(c.Host, "/ @\r\n") {
		return errors.New("mail.host is a host name such as smtp.example.com")
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("mail.port is a port number")
	}
	switch c.sec() {
	case StartTLS, TLS:
	case None:
		if !c.AllowPlain {
			return errors.New("mail.security none sends mail and passwords unencrypted; set mail.allow_plain to say that is acceptable on this network")
		}
	default:
		return fmt.Errorf("mail.security is %s, %s or %s", StartTLS, TLS, None)
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return errors.New("mail.from is an address such as Launchway <notify@example.com>")
	}
	if (c.Username == nil) != (c.Password == nil) {
		return errors.New("mail.username and mail.password go together")
	}
	return nil
}

func (c Config) sec() string {
	if c.Security == "" {
		return StartTLS
	}
	return c.Security
}

// EffectivePort is the port that is connected to: the one set, or the one the security implies.
func (c Config) EffectivePort() int { return c.port() }

func (c Config) port() int {
	switch {
	case c.Port != 0:
		return c.Port
	case c.sec() == TLS:
		return 465
	case c.sec() == None:
		return 25
	}
	return 587
}

// Sender sends messages.
type Sender struct {
	Config  Config
	Secrets Secrets
	Dial    Dialer
}

// ErrNotConfigured is returned when there is no mail server.
var ErrNotConfigured = errors.New("no mail server is configured (the mail section of the installation settings)")

// Send delivers one message now.
func (s *Sender) Send(ctx context.Context, m Message) error {
	c := s.Config
	if !c.Enabled() {
		return ErrNotConfigured
	}
	if err := c.Check(); err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return err
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("the recipient address is not valid")
	}
	user, pass, err := s.credentials(ctx)
	if err != nil {
		return err
	}
	body, err := Build(from, to, m, time.Now())
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.port()))
	dial := s.Dial
	if dial == nil {
		d := net.Dialer{Timeout: timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("could not connect to the mail server: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	tc := c.TLSConfig
	if tc == nil {
		tc = &tls.Config{}
	}
	tc = tc.Clone()
	tc.ServerName, tc.MinVersion = c.Host, tls.VersionTLS12
	if c.sec() == TLS {
		conn = tls.Client(conn, tc)
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return fmt.Errorf("the mail server did not greet us: %w", err)
	}
	defer cl.Close()
	if c.sec() == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			if !c.AllowPlain {
				return errors.New("the mail server does not offer STARTTLS, so nothing was sent (mail.allow_plain would send unencrypted)")
			}
		} else if err := cl.StartTLS(tc); err != nil {
			return fmt.Errorf("the mail server's encryption could not be started: %w", err)
		}
	}
	if user != "" {
		if err := cl.Auth(smtp.PlainAuth("", user, pass, c.Host)); err != nil {
			return fmt.Errorf("the mail server refused the sign-in: %w", cleanErr(err))
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return cleanErr(err)
	}
	if err := cl.Rcpt(to.Address); err != nil {
		return cleanErr(err)
	}
	w, err := cl.Data()
	if err != nil {
		return cleanErr(err)
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return cleanErr(err)
	}
	return cl.Quit()
}

func (s *Sender) credentials(ctx context.Context) (user, pass string, err error) {
	c := s.Config
	if c.Username == nil {
		return "", "", nil
	}
	if s.Secrets == nil {
		return "", "", errors.New("mail.username is a reference to a secret store, but this server has none configured (ai.secrets)")
	}
	if user, err = s.Secrets.Lookup(ctx, "", *c.Username); err != nil {
		return "", "", errors.New("the mail user name could not be read from its secret store")
	}
	if pass, err = s.Secrets.Lookup(ctx, "", *c.Password); err != nil {
		return "", "", errors.New("the mail password could not be read from its secret store")
	}
	return user, pass, nil
}

// cleanErr keeps a server's reply to one short line.
func cleanErr(err error) error {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return errors.New(s)
}

// Build makes the message's bytes: a multipart/alternative of plain text and
// simple HTML, both quoted-printable, with the headers a receiving system
// expects. Nothing in it refers to a remote resource.
func Build(from, to *mail.Address, m Message, now time.Time) ([]byte, error) {
	subject := strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Subject)), " ")
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", now.UTC().Format(time.RFC1123Z))
	id := m.ID
	if id == "" {
		id = strconv.FormatInt(now.UnixNano(), 36)
	}
	if _, dom, ok := strings.Cut(from.Address, "@"); ok {
		h("Message-ID", "<"+id+"@"+dom+">")
	}
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	h("X-Auto-Response-Suppress", "All")
	for k, v := range m.Headers {
		if strings.ContainsAny(k+v, "\r\n") {
			continue
		}
		h(k, v)
	}
	boundary := "lw-" + strconv.FormatInt(now.UnixNano(), 36)
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	part := func(ctype, body string) error {
		b.WriteString("--" + boundary + "\r\nContent-Type: " + ctype + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		var qp strings.Builder
		w := quotedprintable.NewWriter(&qp)
		if _, err := io.WriteString(w, strings.ReplaceAll(body, "\r\n", "\n")); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		b.WriteString(qp.String() + "\r\n")
		return nil
	}
	if err := part("text/plain", m.Text); err != nil {
		return nil, err
	}
	if m.HTML != "" {
		if err := part("text/html", m.HTML); err != nil {
			return nil, err
		}
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), nil
}
