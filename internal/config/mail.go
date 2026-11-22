package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/mail"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// MailConfig is the e-mail Launchway sends to people (an installation
// setting; see internal/mail and internal/notices). Unset: no mail is sent,
// and everything else works: people still have their inbox.
type MailConfig struct {
	// Host and Port are the mail server (SMTP submission). The server must be
	// a public host or one listed under egress.allow.
	Host string `yaml:"host" json:"host,omitempty"`
	Port int    `yaml:"port" json:"port,omitempty"`
	// Security is starttls (the default, port 587), tls (port 465) or none.
	// none sends mail, and the password, unencrypted and needs allow_plain.
	Security string `yaml:"security" json:"security,omitempty"`
	// AllowPlain says that unencrypted mail is acceptable on this network.
	AllowPlain bool `yaml:"allow_plain" json:"allow_plain,omitempty"`
	// Username and Password are references to a secret store, not values.
	Username *pipeline.SecretRef `yaml:"username" json:"username,omitempty"`
	Password *pipeline.SecretRef `yaml:"password" json:"password,omitempty"`
	// From is the sender, "Launchway <notify@example.com>".
	From string `yaml:"from" json:"from,omitempty"`
	// PublicURL is the installation's public address: links in mail lead
	// there. Without it, mail has no links.
	PublicURL string `yaml:"public_url" json:"public_url,omitempty"`
	// RatePerMinute and RatePerHour limit the whole installation (default 30
	// and 300); messages over the limit wait.
	RatePerMinute int `yaml:"rate_per_minute" json:"rate_per_minute,omitempty"`
	RatePerHour   int `yaml:"rate_per_hour" json:"rate_per_hour,omitempty"`
	// MaxAttempts is how many times a message is tried before it is given up
	// on (default 5).
	MaxAttempts int `yaml:"max_attempts" json:"max_attempts,omitempty"`
	// IncludePrivateDetails lets titles and comments of repositories that are
	// not public travel by mail. Off by default: mail leaves the
	// installation's trust boundary, so for such a repository it carries the
	// repository's name and a link, nothing else.
	IncludePrivateDetails bool `yaml:"include_private_details" json:"include_private_details,omitempty"`
}

// Enabled says whether a mail server is set.
func (m MailConfig) Enabled() bool { return m.Host != "" }

// Sender returns the sender's settings.
func (m MailConfig) Sender() mail.Config {
	return mail.Config{Host: m.Host, Port: m.Port, Security: m.Security, AllowPlain: m.AllowPlain, Username: m.Username, Password: m.Password, From: m.From, Timeout: 30 * time.Second}
}

// Check reports what is wrong with the mail settings.
func (m MailConfig) Check() error {
	if m == (MailConfig{}) {
		return nil
	}
	if m.Host == "" {
		return errors.New("mail: set mail.host, or remove the section")
	}
	if err := m.Sender().Check(); err != nil {
		return err
	}
	if m.PublicURL != "" {
		u, err := url.Parse(m.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(m.PublicURL, " \r\n") {
			return errors.New("mail.public_url is the address people open Launchway at, such as https://launchway.example.com")
		}
	}
	for _, r := range []*pipeline.SecretRef{m.Username, m.Password} {
		if r != nil && (r.Store == "" || r.Key == "") {
			return fmt.Errorf("mail.username and mail.password are references to a secret store (store and key), never the values")
		}
	}
	if m.RatePerMinute < 0 || m.RatePerHour < 0 || m.MaxAttempts < 0 || m.MaxAttempts > 20 {
		return errors.New("mail.rate_per_minute, mail.rate_per_hour and mail.max_attempts are positive numbers (at most 20 attempts)")
	}
	return nil
}

// checkTelling checks what tells people: the notification channels and the mail.
func (c *Config) checkTelling() error {
	if err := c.Notifications.Check(); err != nil {
		return err
	}
	return c.Mail.Check()
}
