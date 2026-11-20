package settings

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/redact"
)

// A configuration object is stored in a history that cannot be scrubbed,
// is shown by GET /config, exported and diffed. It must therefore never
// hold a secret value, only a reference to one (a secret store key, an
// environment variable name or a file name). These checks refuse the ways a
// value gets in by accident: a credential in an address, and a credential
// in a header.

const useReference = "use a credential reference instead: the server's own Git credentials (an SSH key or credential helper), or a key in a secret store; never the secret itself, because the configuration's history cannot be scrubbed"

// checkAddress refuses an address that carries a credential: user:password
// or a token before the host, or a token-style query parameter. field names
// the setting for the message.
func checkAddress(field, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	if !strings.Contains(addr, "://") {
		// scp-like (git@host:path) carries a login, never a password.
		if at := strings.Index(addr, "@"); at > 0 && !strings.Contains(addr[:at], "/") && strings.Contains(addr[:at], ":") {
			return fmt.Errorf("%s: the address carries a password; %s", field, useReference)
		}
		return nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		// Not parseable: do not echo it, it may hold what we are looking for.
		return fmt.Errorf("%s: this is not a valid address", field)
	}
	if u.User != nil {
		_, hasPassword := u.User.Password()
		login := (u.Scheme == "ssh" || u.Scheme == "git+ssh" || u.Scheme == "ssh+git") && !hasPassword
		if !login {
			return fmt.Errorf("%s: the address carries a user name or a token before the host (%s); %s", field, redact.URLs(addr), useReference)
		}
	}
	for name := range u.Query() {
		if redact.SecretQueryName(name) {
			return fmt.Errorf("%s: the address carries %q in its query, which looks like a credential; %s", field, name, useReference)
		}
	}
	return nil
}

// secretHeader matches the name of a header that carries a credential.
var secretHeader = regexp.MustCompile(`(?i)^(authorization|proxy-authorization|cookie|x-api-key|api-key|.*token.*|.*secret.*|.*password.*|.*key)$`)

// checkHeaders refuses headers that carry credentials: they are stored in
// the open. A key goes in key:, a reference to a secret store.
func checkHeaders(field string, h map[string]string) error {
	var errs []error
	for name := range h {
		if secretHeader.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: the header %q carries a credential; put the key in key: as a reference to a secret store", field, name))
		}
	}
	return errors.Join(errs...)
}
