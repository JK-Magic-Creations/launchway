package signing

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// The SSH signature format (PROTOCOL.sshsig): an armored blob that carries
// the signer's public key, a namespace (git uses "git") and a signature over
// a hash of the message.

const sshsigMagic = "SSHSIG"

// Namespace is the namespace Git signs commits and tags in.
const Namespace = "git"

type sshsigBlob struct {
	Version   uint32
	PublicKey []byte
	Namespace string
	Reserved  string
	HashAlg   string
	Signature []byte
}

type sshsigSigned struct {
	Namespace string
	Reserved  string
	HashAlg   string
	Hash      []byte
}

// SSHSignature is a parsed SSH signature.
type SSHSignature struct {
	Key       ssh.PublicKey
	Namespace string
	HashAlg   string
	sig       *ssh.Signature
}

// Fingerprint is the signer's key fingerprint (SHA256:...).
func (s *SSHSignature) Fingerprint() string { return ssh.FingerprintSHA256(s.Key) }

// ParseSSHSignature reads an armored SSH signature.
func ParseSSHSignature(armored []byte) (*SSHSignature, error) {
	text := strings.TrimSpace(string(armored))
	text, ok := strings.CutPrefix(text, sshBegin)
	if !ok {
		return nil, errors.New("not an SSH signature")
	}
	text, ok = strings.CutSuffix(strings.TrimSpace(text), "-----END SSH SIGNATURE-----")
	if !ok {
		return nil, errors.New("the SSH signature has no end line")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
	if err != nil {
		return nil, fmt.Errorf("the SSH signature is not valid base64: %w", err)
	}
	rest, ok := bytes.CutPrefix(raw, []byte(sshsigMagic))
	if !ok {
		return nil, errors.New("the SSH signature has no SSHSIG header")
	}
	var b sshsigBlob
	if err := ssh.Unmarshal(rest, &b); err != nil {
		return nil, fmt.Errorf("the SSH signature is malformed: %w", err)
	}
	if b.Version != 1 {
		return nil, fmt.Errorf("SSH signature version %d is not supported", b.Version)
	}
	pk, err := ssh.ParsePublicKey(b.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("the signer's key cannot be read: %w", err)
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(b.Signature, &sig); err != nil {
		return nil, fmt.Errorf("the signature value is malformed: %w", err)
	}
	return &SSHSignature{Key: pk, Namespace: b.Namespace, HashAlg: b.HashAlg, sig: &sig}, nil
}

func hashFor(alg string, msg []byte) ([]byte, error) {
	switch alg {
	case "sha256":
		h := sha256.Sum256(msg)
		return h[:], nil
	case "sha512":
		h := sha512.Sum512(msg)
		return h[:], nil
	}
	return nil, fmt.Errorf("hash %q is not supported", alg)
}

func signedData(ns, alg string, hash []byte) []byte {
	return append([]byte(sshsigMagic), ssh.Marshal(sshsigSigned{Namespace: ns, HashAlg: alg, Hash: hash})...)
}

// Verify checks the signature over a payload against the key it carries. It
// says nothing of whether anyone trusts that key.
func (s *SSHSignature) Verify(payload []byte) error {
	if s.Namespace != Namespace {
		return fmt.Errorf("the signature was made for %q, not for Git", s.Namespace)
	}
	h, err := hashFor(s.HashAlg, payload)
	if err != nil {
		return err
	}
	return s.Key.Verify(signedData(s.Namespace, s.HashAlg, h), s.sig)
}

// SignSSH makes an armored SSH signature over a payload, as ssh-keygen -Y
// sign -n git does.
func SignSSH(signer ssh.Signer, payload []byte) ([]byte, error) {
	h, err := hashFor("sha512", payload)
	if err != nil {
		return nil, err
	}
	sig, err := signer.Sign(rand.Reader, signedData(Namespace, "sha512", h))
	if err != nil {
		return nil, err
	}
	blob := append([]byte(sshsigMagic), ssh.Marshal(sshsigBlob{Version: 1, PublicKey: signer.PublicKey().Marshal(),
		Namespace: Namespace, HashAlg: "sha512", Signature: ssh.Marshal(sig)})...)
	enc := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(sshBegin + "\n")
	for len(enc) > 70 {
		b.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	b.WriteString(enc + "\n-----END SSH SIGNATURE-----\n")
	return []byte(b.String()), nil
}
