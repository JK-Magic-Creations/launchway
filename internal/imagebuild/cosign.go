package imagebuild

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/sigstore/sigstore/pkg/signature/dsse"
)

// Signatures and attestations in the form cosign reads with a key:
//
//   - a signature is a "simple signing" payload naming the image's digest,
//     signed with the key; it is a layer of the image tagged
//     sha256-<digest>.sig in the image's repository, with the signature in
//     the annotation dev.cosignproject.cosign/signature;
//   - an attestation is an in-toto statement about the image (provenance,
//     the SBOM), in a DSSE envelope signed with the key; it is a layer of
//     the image tagged sha256-<digest>.att.
//
// `cosign verify --key cosign.pub --insecure-ignore-tlog` and `cosign
// verify-attestation --key cosign.pub --type slsaprovenance1
// --insecure-ignore-tlog` check them. Nothing is sent to a transparency log
// (Rekor): the key's owner vouches for the image, inside the
// organisation. Keyless signing, with a short-lived certificate from an
// OIDC identity and a transparency log entry, is designed for (SignKeyless)
// but not built.
//
// The crypto is sigstore's own library (the one cosign uses): the key
// formats, the hash and the DSSE encoding are exactly cosign's.

const (
	annoSignature      = "dev.cosignproject.cosign/signature"
	annoPredicateType  = "predicateType"
	mediaSimpleSigning = types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json")
	mediaDSSE          = types.MediaType("application/vnd.dsse.envelope.v1+json")
	inTotoPayloadType  = "application/vnd.in-toto+json"
)

// signer signs with a cosign key.
type signer struct {
	sv signature.SignerVerifier
	// PublicKey is the public key in PEM, and KeyID a short fingerprint of
	// it (the first 16 hex digits of the SHA-256 of its DER), recorded so
	// people can tell which key signed.
	PublicKey string
	KeyID     string
}

// loadSigner reads a private key as cosign writes it (an encrypted
// "SIGSTORE PRIVATE KEY", given its password) or as PKCS#8 or SEC 1 PEM.
// Only ECDSA keys are taken, as cosign makes them.
func loadSigner(pemBytes, password []byte) (*signer, error) {
	if !bytes.Contains(pemBytes, []byte("-----BEGIN")) {
		return nil, errors.New("the signing key is not in PEM form (-----BEGIN ...)")
	}
	priv, err := cryptoutils.UnmarshalPEMToPrivateKey(pemBytes, cryptoutils.StaticPasswordFunc(password))
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "decrypt") {
			return nil, errors.New("the signing key could not be decrypted: wrong password (sign.password)")
		}
		return nil, fmt.Errorf("the signing key could not be read: %s", msg)
	}
	ec, ok := priv.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the signing key is a %T; Launchway signs with ECDSA keys, as `cosign generate-key-pair` makes", priv)
	}
	sv, err := signature.LoadSignerVerifier(ec, crypto.SHA256)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		return nil, err
	}
	pubPEM, err := cryptoutils.MarshalPublicKeyToPEM(&ec.PublicKey)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(der)
	return &signer{sv: sv, PublicKey: string(pubPEM), KeyID: hex.EncodeToString(sum[:8])}, nil
}

// simpleSigning is cosign's signature payload.
type simpleSigning struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional map[string]any `json:"optional"`
}

// sigTag is the tag cosign keeps an image's signatures (suffix sig) or
// attestations (att) under.
func sigTag(r *remoteRegistry, repo string, digest v1.Hash, suffix string) (name.Tag, error) {
	return r.tag(repo, digest.Algorithm+"-"+digest.Hex+"."+suffix)
}

// sign signs an image's digest and adds the signature to the repository.
func (s *signer) sign(ctx context.Context, r *remoteRegistry, repo string, digest v1.Hash) (string, error) {
	var p simpleSigning
	p.Critical.Identity.DockerReference = r.cfg.Host + "/" + repo
	p.Critical.Image.DockerManifestDigest = digest.String()
	p.Critical.Type = "cosign container image signature"
	payload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig, err := s.sv.SignMessage(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	layer := static.NewLayer(payload, mediaSimpleSigning)
	tag, err := sigTag(r, repo, digest, "sig")
	if err != nil {
		return "", err
	}
	if err := appendLayer(ctx, r, tag, layer, map[string]string{annoSignature: base64.StdEncoding.EncodeToString(sig)}); err != nil {
		return "", r.explain("adding the signature", err)
	}
	return tag.String(), nil
}

// attest signs an in-toto statement about an image and adds it to the
// repository as an attestation.
func (s *signer) attest(ctx context.Context, r *remoteRegistry, repo string, digest v1.Hash, predicateType string, stmt []byte) (string, error) {
	env, err := dsse.WrapSigner(s.sv, inTotoPayloadType).SignMessage(bytes.NewReader(stmt))
	if err != nil {
		return "", err
	}
	layer := static.NewLayer(env, mediaDSSE)
	tag, err := sigTag(r, repo, digest, "att")
	if err != nil {
		return "", err
	}
	if err := appendLayer(ctx, r, tag, layer, map[string]string{annoSignature: "", annoPredicateType: predicateType}); err != nil {
		return "", r.explain("adding an attestation", err)
	}
	return tag.String(), nil
}

// appendLayer adds a layer to the image at tag, making the image if it is
// not there, as cosign does: signing the same image twice keeps both.
func appendLayer(ctx context.Context, r *remoteRegistry, tag name.Tag, layer v1.Layer, annotations map[string]string) error {
	opts := r.options(ctx)
	base, err := remote.Image(tag, opts...)
	var te *transport.Error
	switch {
	case err == nil:
	case errors.As(err, &te) && te.StatusCode == http.StatusNotFound:
		base = mutate.ConfigMediaType(mutate.MediaType(empty.Image, types.OCIManifestSchema1), types.OCIConfigJSON)
	default:
		return err
	}
	dg, err := layer.Digest()
	if err != nil {
		return err
	}
	if m, err := base.Manifest(); err == nil {
		for _, l := range m.Layers {
			if l.Digest == dg && l.Annotations[annoSignature] == annotations[annoSignature] {
				return nil // this very signature is there already
			}
		}
	}
	img, err := mutate.Append(base, mutate.Addendum{Layer: layer, Annotations: annotations})
	if err != nil {
		return err
	}
	return remote.Write(tag, img, opts...)
}
