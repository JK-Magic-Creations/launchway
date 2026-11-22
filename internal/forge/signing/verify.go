package signing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// State is the verdict on one object.
type State string

// The states, in the order a person meets them.
const (
	Verified     State = "verified"      // signed by a registered key that was good at the time
	UnknownKey   State = "unknown_key"   // a good signature, by a key nobody registered
	BadSignature State = "bad_signature" // the signature does not match the object
	Unsigned     State = "unsigned"
	KeyRevoked   State = "key_revoked"
	KeyExpired   State = "key_expired"
	Unsupported  State = "unsupported" // signed in a format this build cannot check
)

// Result is what is known about the signature of one commit or tag.
type Result struct {
	Object        string `json:"object"`
	Kind          string `json:"kind"`
	State         State  `json:"state"`
	Explanation   string `json:"explanation"`
	SignatureType string `json:"signature_type,omitempty"`
	// KeyFingerprint is the key the signature names, whoever owns it.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	KeyName        string `json:"key_name,omitempty"`
	// Signer is the person who registered the key ("launchway" for the
	// installation's own key).
	Signer            string    `json:"signer,omitempty"`
	Installation      bool      `json:"installation_key,omitempty"`
	SignedAt          time.Time `json:"signed_at,omitzero"`
	SignerIsAuthor    bool      `json:"signer_is_author,omitempty"`
	SignerIsCommitter bool      `json:"signer_is_committer,omitempty"`
	SignerIsPusher    bool      `json:"signer_is_pusher,omitempty"`
}

// OK says whether the object counts as verified for a rule.
func (r Result) OK() bool { return r.State == Verified }

// Key is a registered signing key.
type Key struct {
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at,omitzero"`
	RevokedAt   time.Time `json:"revoked_at,omitzero"`
	// Installation marks the installation's own signing key.
	Installation bool `json:"installation_key,omitempty"`
}

// Cached is the cryptographic part of a verdict, which never changes for an
// object.
type Cached struct {
	Object      string
	SigType     string
	Fingerprint string
	CryptoOK    bool
	Reason      string
}

// Directory is what the verifier asks of the installation.
type Directory interface {
	// KeyByFingerprint finds a registered key (or the installation's own).
	KeyByFingerprint(ctx context.Context, fp string) (Key, bool, error)
	// Emails lists the addresses a person commits under.
	Emails(ctx context.Context, user string) []string
	GetCached(ctx context.Context, object string) (Cached, bool)
	PutCached(ctx context.Context, c Cached)
}

// Verifier judges objects.
type Verifier struct {
	Dir Directory
	Now func() time.Time
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify judges a raw commit or tag object. pusher, when not empty, is the
// person who pushed it (for the "signer is the pusher" note).
func (v *Verifier) Verify(ctx context.Context, sha, kind string, raw []byte, pusher string) Result {
	res := Result{Object: sha, Kind: kind}
	p, err := Parse(kind, raw)
	if err != nil {
		res.State, res.Explanation = BadSignature, "This object could not be read, so its signature could not be checked."
		return res
	}
	res.SignedAt = p.Committer.At
	if !p.Signed() {
		res.State = Unsigned
		res.Explanation = "This " + kind + " carries no signature, so nothing shows who made it: anyone can write any name in the author line."
		return res
	}
	res.SignatureType = p.SigType
	var (
		c      Cached
		native State // what the key's own data says of a good signature (revoked, expired); empty when fine
	)
	key, found, kerr := Key{}, false, error(nil)
	switch p.SigType {
	case "ssh":
		var ok bool
		if c, ok = v.Dir.GetCached(ctx, sha); !ok {
			c = Cached{Object: sha, SigType: "ssh"}
			sig, perr := ParseSSHSignature(p.Signature)
			if perr != nil {
				c.Reason = perr.Error()
			} else {
				c.Fingerprint = sig.Fingerprint()
				if verr := sig.Verify(p.Payload); verr != nil {
					c.Reason = verr.Error()
				} else {
					c.CryptoOK = true
				}
			}
			v.Dir.PutCached(ctx, c)
		}
		if c.Fingerprint != "" {
			key, found, kerr = v.Dir.KeyByFingerprint(ctx, c.Fingerprint)
		}
	case "openpgp":
		pv := v.checkOpenPGP(ctx, sha, kind, p, &res)
		if pv.done {
			return res
		}
		c, key, found, kerr, native = pv.c, pv.key, pv.found, pv.kerr, pv.native
	default:
		res.State = Unsupported
		res.Explanation = fmt.Sprintf("This %s is signed with %s, which this Launchway does not check (SSH and OpenPGP signatures are checked). It is not counted as verified.", kind, sigLabel(p.SigType))
		return res
	}
	res.KeyFingerprint = c.Fingerprint
	if found {
		res.KeyName, res.Signer, res.Installation = key.Name, key.Owner, key.Installation
	}
	if p.SigType == "openpgp" && !found && kerr == nil && c.Reason == "" {
		res.State = UnknownKey
		res.Explanation = "This " + kind + " is signed with the OpenPGP key " + c.Fingerprint + ", which nobody has registered on Launchway, so the signature cannot be checked or tied to a person. The key's owner can register the public key under Account, then Signing keys."
		return res
	}
	if !c.CryptoOK {
		res.State = BadSignature
		res.Explanation = "The signature does not match this " + kind + ": it was changed after it was signed, or the signature is damaged (" + c.Reason + "). Do not trust it."
		return res
	}
	if kerr != nil {
		res.State = UnknownKey
		res.Explanation = "The signature is good, but the registered keys could not be read just now, so the signer is unknown."
		return res
	}
	if !found {
		res.State = UnknownKey
		res.Explanation = "The signature is good, but it was made by a key (" + c.Fingerprint + ") that nobody has registered on Launchway, so it cannot be tied to a person. The key's owner can register it under Account, then Signing keys."
		return res
	}
	switch native {
	case KeyRevoked:
		res.State = KeyRevoked
		res.Explanation = fmt.Sprintf("The signature is good, but the key %q (%s) is revoked in its own data, and the revocation applies to a %s of this date.", key.Name, key.Owner, kind)
		return res
	case KeyExpired:
		res.State = KeyExpired
		res.Explanation = fmt.Sprintf("The signature is good, but the key %q (%s) had expired, by its own data, when this %s was signed.", key.Name, key.Owner, kind)
		return res
	}
	at := p.Committer.At
	if at.IsZero() {
		at = v.now()
	}
	switch {
	case !key.RevokedAt.IsZero() && !at.Before(key.RevokedAt):
		res.State = KeyRevoked
		res.Explanation = fmt.Sprintf("The signature is good, but %s revoked the key %q on %s, and this %s is dated after that.", key.Owner, key.Name, key.RevokedAt.Format("2006-01-02"), kind)
		return res
	case !key.ExpiresAt.IsZero() && !at.Before(key.ExpiresAt):
		res.State = KeyExpired
		res.Explanation = fmt.Sprintf("The signature is good, but the key %q (%s) had expired on %s, before this %s was dated.", key.Name, key.Owner, key.ExpiresAt.Format("2006-01-02"), kind)
		return res
	}
	res.State = Verified
	emails := v.Dir.Emails(ctx, key.Owner)
	res.SignerIsAuthor = hasEmail(emails, p.Author.Email)
	res.SignerIsCommitter = hasEmail(emails, p.Committer.Email)
	res.SignerIsPusher = pusher != "" && strings.EqualFold(pusher, key.Owner)
	who := "@" + key.Owner
	if key.Installation {
		who = "Launchway itself (the installation's signing key)"
	}
	res.Explanation = fmt.Sprintf("Signed by %s with the key %q (%s). The signature matches the %s exactly as it is now.", who, key.Name, c.Fingerprint, kind)
	switch {
	case key.Installation:
	case res.SignerIsAuthor:
		res.Explanation += " The signer is also the author."
	case res.SignerIsCommitter:
		res.Explanation += " The signer is the committer, not the author."
	default:
		res.Explanation += " The signer's registered addresses do not include the author's or the committer's, so the signature vouches for the content but not for the name in the author line."
	}
	return res
}

func sigLabel(t string) string {
	switch t {
	case "openpgp":
		return "OpenPGP"
	case "x509":
		return "an X.509 certificate"
	}
	return "a format Launchway does not know"
}

func hasEmail(list []string, e string) bool {
	if e == "" {
		return false
	}
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), e) {
			return true
		}
	}
	return false
}

// checkOpenPGP judges an OpenPGP signature: the signature does not carry its key, so the registered key is found by the
// fingerprint the signature names, and the check is made against it (and not cached, because the answer depends on the
// key). done is true when the verdict is already in res.
func (v *Verifier) checkOpenPGP(ctx context.Context, sha, kind string, p Parsed, res *Result) (out pgpVerdict) {
	c, key, found, kerr, native := Cached{Object: sha, SigType: "openpgp"}, Key{}, false, error(nil), State("")
	sig, perr := parsePGPSignature(p.Signature)
	switch {
	case perr != nil:
		c.Reason = perr.Error()
	case sig.fingerprint == "":
		res.State = UnknownKey
		res.Explanation = "This " + kind + " is signed with OpenPGP, but the signature does not name its key by fingerprint, so Launchway cannot tell which registered key to check it against."
		return pgpVerdict{c: c, done: true}
	default:
		c.Fingerprint = sig.fingerprint
		key, found, kerr = v.Dir.KeyByFingerprint(ctx, sig.fingerprint)
		if found && kerr == nil {
			st, cerr := checkPGP(key.PublicKey, p.Signature, p.Payload, sig.at)
			switch st {
			case Verified:
				c.CryptoOK = true
			case KeyRevoked, KeyExpired:
				c.CryptoOK, native = true, st
			default:
				c.Reason = cerr.Error()
			}
		}
	}
	return pgpVerdict{c: c, key: key, found: found, kerr: kerr, native: native}
}

// pgpVerdict is what checkOpenPGP found out.
type pgpVerdict struct {
	c      Cached
	key    Key
	found  bool
	kerr   error
	native State
	done   bool // the verdict is already in the result
}
