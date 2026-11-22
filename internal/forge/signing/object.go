package signing

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Ident is the name, address and time of an author, committer or tagger line.
type Ident struct {
	Name  string
	Email string
	At    time.Time
}

// Parsed is what the verifier needs from a commit or an annotated tag.
type Parsed struct {
	Kind      string // commit or tag
	Author    Ident  // a commit's author, a tag's tagger
	Committer Ident  // a commit's committer; the tagger again for a tag
	// SigType is ssh, openpgp, x509 or unknown; empty when unsigned.
	SigType string
	// Signature is the armored signature as written in the object.
	Signature []byte
	// Payload is what was signed.
	Payload []byte
}

// Signed reports whether the object carries a signature.
func (p Parsed) Signed() bool { return len(p.Signature) > 0 }

const (
	sshBegin = "-----BEGIN SSH SIGNATURE-----"
	pgpBegin = "-----BEGIN PGP SIGNATURE-----"
	x509Beg  = "-----BEGIN SIGNED MESSAGE-----"
)

func sigType(sig []byte) string {
	s := strings.TrimSpace(string(sig))
	switch {
	case strings.HasPrefix(s, sshBegin):
		return "ssh"
	case strings.HasPrefix(s, pgpBegin):
		return "openpgp"
	case strings.HasPrefix(s, x509Beg):
		return "x509"
	}
	return "unknown"
}

// ErrNotAnObject is returned for bytes that are not a commit or a tag.
var ErrNotAnObject = errors.New("not a commit or a tag")

// Parse reads a raw commit or tag object (as git cat-file prints it, without
// its "commit <size>" prefix).
func Parse(kind string, raw []byte) (Parsed, error) {
	if kind != "commit" && kind != "tag" {
		return Parsed{}, ErrNotAnObject
	}
	head, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		// A tag or commit with no message still ends its headers with a newline.
		head, body = bytes.TrimSuffix(raw, []byte("\n")), nil
	}
	p := Parsed{Kind: kind}
	lines := strings.Split(string(head), "\n")
	var kept []string
	var sig []string
	inSig := false
	sigDone := false
	for _, l := range lines {
		if inSig && strings.HasPrefix(l, " ") {
			sig = append(sig, l[1:])
			continue
		}
		inSig = false
		name, val, _ := strings.Cut(l, " ")
		switch {
		case kind == "commit" && (name == "gpgsig" || name == "gpgsig-sha256") && !sigDone:
			inSig, sigDone = true, true
			sig = append(sig, val)
			continue
		case name == "author" && kind == "commit":
			p.Author = parseIdent(val)
		case name == "committer":
			p.Committer = parseIdent(val)
		case name == "tagger":
			p.Author = parseIdent(val)
			p.Committer = p.Author
		}
		kept = append(kept, l)
	}
	if kind == "commit" {
		p.Payload = raw // what is signed, when nothing is signed yet: the whole object
		if sigDone {
			p.Signature = []byte(strings.Join(sig, "\n") + "\n")
			p.SigType = sigType(p.Signature)
			p.Payload = []byte(strings.Join(kept, "\n") + "\n\n" + string(body))
		}
		return p, nil
	}
	// A tag: the signature is the last block of the message.
	msg := string(body)
	at := -1
	for _, b := range []string{sshBegin, pgpBegin, x509Beg} {
		if i := indexLine(msg, b); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at >= 0 {
		p.Signature = []byte(msg[at:])
		p.SigType = sigType(p.Signature)
		p.Payload = []byte(string(head) + "\n\n" + msg[:at])
	}
	return p, nil
}

// indexLine finds s at the start of a line.
func indexLine(msg, s string) int {
	if strings.HasPrefix(msg, s) {
		return 0
	}
	if i := strings.Index(msg, "\n"+s); i >= 0 {
		return i + 1
	}
	return -1
}

func parseIdent(v string) Ident {
	var id Ident
	lt, gt := strings.LastIndexByte(v, '<'), strings.LastIndexByte(v, '>')
	if lt < 0 || gt < lt {
		id.Name = strings.TrimSpace(v)
		return id
	}
	id.Name = strings.TrimSpace(v[:lt])
	id.Email = v[lt+1 : gt]
	f := strings.Fields(v[gt+1:])
	if len(f) > 0 {
		if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
			id.At = time.Unix(n, 0).UTC()
		}
	}
	return id
}

// AddCommitSignature returns a raw commit with an armored signature added as
// its gpgsig header, as git writes it: after the other headers, each line of
// the signature continued with a leading space.
func AddCommitSignature(raw, armored []byte) ([]byte, error) {
	head, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		return nil, errors.New("not a commit")
	}
	if bytes.Contains(append([]byte("\n"), head...), []byte("\ngpgsig")) {
		return nil, errors.New("the commit is already signed")
	}
	lines := strings.Split(strings.TrimRight(string(armored), "\n"), "\n")
	var b bytes.Buffer
	b.Write(head)
	b.WriteString("\ngpgsig " + lines[0])
	for _, l := range lines[1:] {
		b.WriteString("\n " + l)
	}
	b.WriteString("\n\n")
	b.Write(body)
	return b.Bytes(), nil
}
