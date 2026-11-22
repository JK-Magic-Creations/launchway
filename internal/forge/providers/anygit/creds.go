package anygit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// DefaultUser is the user name sent with a bare token: most servers read the
// token from the password and ignore the name, and say so in their own
// documentation; a server that checks the name needs the JSON form.
const DefaultUser = "git"

// cred is what a connection's credential reference holds.
type cred struct {
	user, secret string
}

// credDoc is the JSON form: {"username": "...", "password": "..."} for a
// server that checks the user name, or {"token": "..."} (with an optional
// "username") for one that takes a token as the password.
type credDoc struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

// parseCred reads the credential. A value that does not begin with "{" is a
// token, sent as the HTTP Basic password with the user name "git". A JSON
// document is strict: only the three fields, a password or a token (not both),
// and no control characters. Errors never repeat the value.
func parseCred(raw string) (cred, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cred{}, errors.New("the credential is empty")
	}
	if !strings.HasPrefix(raw, "{") {
		if strings.ContainsAny(raw, "\r\n\x00") {
			return cred{}, errors.New("a token is one line; for a user name and a password give a JSON document")
		}
		return cred{user: DefaultUser, secret: raw}, nil
	}
	var d credDoc
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil || dec.More() {
		return cred{}, errors.New("the credential is not a valid JSON document: it holds username and password, or token (and nothing else)")
	}
	secret := d.Password
	switch {
	case d.Password != "" && d.Token != "":
		return cred{}, errors.New("the credential holds a password or a token, not both")
	case d.Token != "":
		secret = d.Token
	case d.Password == "":
		return cred{}, errors.New("the credential document has no password or token")
	}
	user := d.Username
	if user == "" {
		user = DefaultUser
	}
	if strings.ContainsAny(user+secret, "\r\n\x00") || strings.Contains(user, ":") {
		return cred{}, errors.New("the user name has no ':' and neither field has a line break")
	}
	return cred{user: user, secret: secret}, nil
}
