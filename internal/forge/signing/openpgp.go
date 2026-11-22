package signing

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	pgparmor "github.com/ProtonMail/go-crypto/openpgp/armor"
	pgperr "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// OpenPGP signatures (what "git commit -S" makes with gpg). A person registers the public key block; a
// signature names the key that made it by fingerprint, which may be the primary key or one of its signing
// subkeys, so each signing subkey is stored beside the primary key as a row of its own (kind "openpgp-subkey",
// the same block as its text) that follows the primary key's revoking and deleting and is not listed.
// X.509 signatures are not checked: they stay "not checked".

// Kinds of signing key rows that hold OpenPGP keys.
const (
	kindPGP    = "openpgp"
	kindPGPSub = "openpgp-subkey"
)

// maxPGPKeyText bounds a pasted key block.
const maxPGPKeyText = 256 << 10

// PGPKey is a parsed public OpenPGP key.
type PGPKey struct {
	Armored     string
	Fingerprint string   // the primary key's, upper-case hex
	Subkeys     []string // fingerprints of the signing subkeys, upper-case hex
	Name        string   // the first identity, for a default name
	Algorithm   string
}

func fpHex(b []byte) string { return strings.ToUpper(hex.EncodeToString(b)) }

// ParsePGPKey reads a pasted public key block and says what it holds. It refuses private keys, several keys at
// once, revoked or expired keys, and weak ones.
func ParsePGPKey(text string, now time.Time) (*PGPKey, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "PRIVATE KEY BLOCK") {
		return nil, fmt.Errorf("%w: that is a PRIVATE key. Never paste it anywhere; export the public key (gpg --armor --export <id>)", ErrInvalid)
	}
	if len(text) > maxPGPKeyText {
		return nil, fmt.Errorf("%w: that key block is too large", ErrInvalid)
	}
	if strings.Count(text, "BEGIN PGP PUBLIC KEY BLOCK") > 1 {
		return nil, fmt.Errorf("%w: paste one key at a time", ErrInvalid)
	}
	list, err := pgp.ReadArmoredKeyRing(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("%w: that is not an OpenPGP public key block (%v)", ErrInvalid, err)
	}
	if len(list) != 1 {
		return nil, fmt.Errorf("%w: paste one key at a time (this holds %d)", ErrInvalid, len(list))
	}
	e := list[0]
	if e.PrivateKey != nil {
		return nil, fmt.Errorf("%w: that is a private key", ErrInvalid)
	}
	pk := e.PrimaryKey
	if e.Revoked(now) {
		return nil, fmt.Errorf("%w: that key is revoked", ErrInvalid)
	}
	if self, _ := e.PrimarySelfSignature(); self == nil || pk.KeyExpired(self, now) {
		return nil, fmt.Errorf("%w: that key has expired (or has no valid identity)", ErrInvalid)
	}
	k := &PGPKey{Armored: text, Fingerprint: fpHex(pk.Fingerprint)}
	if bits, err := pk.BitLength(); err == nil && pk.PubKeyAlgo.CanSign() {
		switch pk.PubKeyAlgo {
		case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSASignOnly:
			k.Algorithm = fmt.Sprintf("rsa%d", bits)
			if bits < MinRSABits {
				return nil, fmt.Errorf("%w: RSA keys must be at least %d bits", ErrInvalid, MinRSABits)
			}
		case packet.PubKeyAlgoECDSA:
			k.Algorithm = "ecdsa"
		case packet.PubKeyAlgoEdDSA, packet.PubKeyAlgoEd25519, packet.PubKeyAlgoEd448:
			k.Algorithm = "ed25519"
		default:
			return nil, fmt.Errorf("%w: that kind of key (DSA or another old one) is not accepted; use RSA of %d bits or more, ECDSA or Ed25519", ErrInvalid, MinRSABits)
		}
	} else {
		return nil, fmt.Errorf("%w: that key cannot sign", ErrInvalid)
	}
	for name := range e.Identities {
		k.Name = name
		break
	}
	for _, sub := range e.Subkeys {
		if sub.Sig == nil || !sub.Sig.FlagsValid || !sub.Sig.FlagSign || sub.Revoked(now) || sub.PublicKey.KeyExpired(sub.Sig, now) {
			continue
		}
		k.Subkeys = append(k.Subkeys, fpHex(sub.PublicKey.Fingerprint))
	}
	return k, nil
}

// pgpSig is what a signature says about itself.
type pgpSig struct {
	fingerprint string // of the key that made it (primary or subkey), upper-case hex; empty when it names none
	at          time.Time
	sig         *packet.Signature
}

// parsePGPSignature reads an armored OpenPGP signature.
func parsePGPSignature(armored []byte) (*pgpSig, error) {
	blk, err := pgparmor.Decode(bytes.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("the signature is not readable (%v)", err)
	}
	p, err := packet.Read(blk.Body)
	if err != nil {
		return nil, fmt.Errorf("the signature is not readable (%v)", err)
	}
	sig, ok := p.(*packet.Signature)
	if !ok {
		return nil, errors.New("the signature block holds something that is not a signature")
	}
	s := &pgpSig{at: sig.CreationTime, sig: sig}
	if len(sig.IssuerFingerprint) > 0 {
		s.fingerprint = fpHex(sig.IssuerFingerprint)
	}
	return s, nil
}

// checkPGP verifies a detached signature of payload against a registered key block, judging the key's own
// validity (revoked, expired, at the time the signature was made). A nil error means a good signature by a
// good key; KeyRevoked and KeyExpired say which way a good signature by a bad key failed.
func checkPGP(armoredKey string, armoredSig, payload []byte, at time.Time) (State, error) {
	ring, err := pgp.ReadArmoredKeyRing(strings.NewReader(armoredKey))
	if err != nil {
		return BadSignature, fmt.Errorf("the registered key cannot be read (%v)", err)
	}
	cfg := &packet.Config{Time: func() time.Time { return at }}
	_, err = pgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(payload), bytes.NewReader(armoredSig), cfg)
	switch {
	case err == nil:
		return Verified, nil
	case errors.Is(err, pgperr.ErrKeyRevoked):
		return KeyRevoked, err
	case errors.Is(err, pgperr.ErrKeyExpired):
		return KeyExpired, err
	case errors.Is(err, pgperr.ErrUnknownIssuer):
		return UnknownKey, err
	}
	return BadSignature, err
}
