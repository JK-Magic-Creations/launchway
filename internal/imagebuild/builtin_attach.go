package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// What an image pushed to the built-in registry carries, as OCI referrers
// (manifests whose subject is the image), so that the Images tab, the CLI and
// any client that lists referrers can show them without reading a report:
//
//   - the vulnerability scan's counts, as annotations of a manifest of type
//     ScanArtifactType (the report itself is its layer), and
//   - each platform's SBOM, as a manifest of type SBOMArtifactType.
//
// Signatures and attestations are cosign's own tags (see cosign.go).
// The annotation keys and types are the registry's (internal/registry).
const (
	ScanArtifactType = "application/vnd.launchway.scan.v1+json"
	SBOMArtifactType = "application/vnd.cyclonedx+json"

	annVulnCritical = "io.launchway.vulnerabilities.critical"
	annVulnHigh     = "io.launchway.vulnerabilities.high"
	annVulnMedium   = "io.launchway.vulnerabilities.medium"
	annVulnLow      = "io.launchway.vulnerabilities.low"
	annScanner      = "io.launchway.scanner"
	annSource       = "org.opencontainers.image.source"
)

// attachReferrer pushes an artifact manifest, with one layer holding content,
// whose subject is digest of the repository.
func attachReferrer(ctx context.Context, r *remoteRegistry, repo string, subject v1.Hash, mediaType types.MediaType, artifactType string, content []byte, annotations map[string]string) error {
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.MediaType(artifactType))
	img, err := mutate.Append(img, mutate.Addendum{Layer: static.NewLayer(content, mediaType)})
	if err != nil {
		return err
	}
	subRef, err := r.digest(repo, subject.String())
	if err != nil {
		return err
	}
	// The subject descriptor says what the subject is, as the registry holds it.
	desc, err := remote.Head(subRef, r.options(ctx)...)
	if err != nil {
		return r.explain("finding "+subject.String()+" to attach to it", err)
	}
	var withAnn any = mutate.Annotations(img, annotations)
	var sub any = mutate.Subject(withAnn.(v1.Image), v1.Descriptor{MediaType: desc.MediaType, Digest: subject, Size: desc.Size})
	out, ok := sub.(v1.Image)
	if !ok {
		return fmt.Errorf("could not build the referrer manifest")
	}
	d, err := out.Digest()
	if err != nil {
		return err
	}
	ref, err := r.digest(repo, d.String())
	if err != nil {
		return err
	}
	return r.explain("attaching to "+subject.String(), remote.Write(ref, out, r.options(ctx)...))
}

// AttachToBuiltin attaches the scan result and the SBOMs of a finished build
// to what it pushed to the built-in registry (the tags that name it). It does
// nothing when the build failed or pushed nothing there. A failure is logged,
// not fatal: the image is pushed and signed either way.
func AttachToBuiltin(ctx context.Context, cfg *Config, req Request, res *Result, buildErr error) {
	if buildErr != nil || res == nil || !res.Pushed || res.Digest == "" {
		return
	}
	log := req.Log
	if log == nil {
		log = func(string) {}
	}
	root, err := v1.NewHash(res.Digest)
	if err != nil {
		return
	}
	rc, err := cfg.Registry(BuiltinName)
	if err != nil {
		return
	}
	user, pass, err := readCredentials(ctx, req.Lookup, rc.Credentials)
	if err != nil {
		log("Could not read the built-in registry's credential to attach the scan result: " + err.Error())
		return
	}
	reg, err := newRemote(BuiltinName, rc, user, pass)
	if err != nil {
		return
	}
	done := map[string]bool{}
	for _, t := range req.Tags {
		if t.Registry != BuiltinName || done[t.Repository] {
			continue
		}
		done[t.Repository] = true
		attachAll(ctx, reg, t.Repository, root, res, req, log)
	}
}

func attachAll(ctx context.Context, reg *remoteRegistry, repo string, root v1.Hash, res *Result, req Request, log func(string)) {
	if s := res.Scan; s != nil {
		report, _ := json.Marshal(s)
		ann := map[string]string{annVulnCritical: strconv.Itoa(s.Critical), annVulnHigh: strconv.Itoa(s.High), annVulnMedium: strconv.Itoa(s.Medium),
			annVulnLow: strconv.Itoa(s.Low), annScanner: "launchway image scan"}
		if err := attachReferrer(ctx, reg, repo, root, ScanArtifactType, ScanArtifactType, report, ann); err != nil {
			log("Could not attach the scan result to the image: " + err.Error())
		} else {
			log("Attached the scan result to the image")
		}
	}
	if !req.SBOM {
		return
	}
	for _, pi := range res.Platforms {
		data, err := os.ReadFile(filepath.Join(req.WorkDir, OutDir, SBOMDir, fileSafe(nonEmptyStr(pi.Platform, "image"))+".cdx.json"))
		if err != nil {
			continue
		}
		d, err := v1.NewHash(pi.Digest)
		if err != nil {
			continue
		}
		if err := attachReferrer(ctx, reg, repo, d, SBOMArtifactType, SBOMArtifactType, data, map[string]string{annSource: req.Source.Repository}); err != nil {
			log("Could not attach the SBOM to the image: " + err.Error())
		}
	}
}
