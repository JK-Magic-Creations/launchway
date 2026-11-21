package giteaaccept

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is what the tests need to reach the host under test.
type Config struct {
	URL, Alt, Token, User, Flavor string
	Receiver, MirrorSrc, OIDCPath string
	BigMB                         int
	repos                         []string // repositories this run created: the only ones it may delete
}

// Launchway reports whether the host under test is meant to be Launchway.
func (c *Config) Launchway() bool { return c.Flavor == "launchway" }

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// loadConfig reads the environment. It returns nil when no host is set.
func loadConfig() *Config {
	u := strings.TrimRight(os.Getenv("LW_ACCEPT_URL"), "/")
	if u == "" {
		return nil
	}
	mb, err := strconv.Atoi(env("LW_ACCEPT_BIGPUSH_MB", "50"))
	if err != nil || mb < 1 {
		mb = 50
	}
	return &Config{
		URL: u, Alt: strings.TrimRight(os.Getenv("LW_ACCEPT_URL_ALT"), "/"),
		Token: os.Getenv("LW_ACCEPT_TOKEN"), Flavor: env("LW_ACCEPT_FLAVOR", "gitea"),
		Receiver: os.Getenv("LW_ACCEPT_RECEIVER"), BigMB: mb,
		MirrorSrc: env("LW_ACCEPT_MIRROR_SRC", "https://github.com/JK-Magic-Creations/launchway.git"),
		OIDCPath:  env("LW_ACCEPT_OIDC_PATH", "/user/oauth2/authentik"),
	}
}

// receiverHost is this machine's address as the host under test sees it.
func (c *Config) receiverHost() (string, error) {
	if c.Receiver != "" {
		return c.Receiver, nil
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return "", err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "80")
	}
	conn, err := net.Dial("udp", host)
	if err != nil {
		return "", fmt.Errorf("cannot find this machine's address towards %s: %w", host, err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// basic is the value of an HTTP Basic Authorization header for the token.
func (c *Config) basic() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.User+":"+c.Token))
}

// scrub removes every form of the token from text that may be shown.
func (c *Config) scrub(s string) string {
	if c.Token == "" {
		return s
	}
	for _, secret := range []string{c.Token, c.basic(), base64.StdEncoding.EncodeToString([]byte(c.User + ":" + c.Token))} {
		s = strings.ReplaceAll(s, secret, "[token]")
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
