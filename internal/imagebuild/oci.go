package imagebuild

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// What a builder sends back is an OCI image layout in a tar: index.json,
// oci-layout and blobs/sha256/<hex>. The runner unpacks it (nothing else is
// accepted in it), reads the image index from it, pushes it, and hands the
// layout to the scanner.

var blobNameRe = regexp.MustCompile(`^blobs/sha256/[0-9a-f]{64}$`)

// unpackLayout unpacks an OCI layout tar into dir.
func unpackLayout(tarPath, dir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o700); err != nil {
		return err
	}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the image the builder sent: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		switch {
		case h.Typeflag == tar.TypeDir:
			continue
		case h.Typeflag != tar.TypeReg:
			return fmt.Errorf("the image the builder sent has an entry %q that is not a file", h.Name)
		case name == "index.json" || name == "oci-layout" || blobNameRe.MatchString(name):
		case name == "manifest.json":
			continue // the Docker form of the same, not needed
		default:
			return fmt.Errorf("the image the builder sent has an unexpected entry %q", h.Name)
		}
		out, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(name)), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

// built is an image a build made, as an OCI layout on the runner.
type built struct {
	dir string
	// root is what index.json points at: an image index (several
	// platforms, or attestations) or a single image manifest.
	root  v1.Descriptor
	index v1.ImageIndex // when root is an index
	image v1.Image      // when root is one image
}

// PlatformImage is one platform's image in what was built.
type PlatformImage struct {
	Platform string `json:"platform"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"` // compressed: manifest, config and layers
}

const (
	annoRefType   = "vnd.docker.reference.type"
	annoRefDigest = "vnd.docker.reference.digest"
	attestation   = "attestation-manifest"
)

func openLayout(dir string) (*built, error) {
	p, err := layout.FromPath(dir)
	if err != nil {
		return nil, fmt.Errorf("the image the builder sent is not an OCI layout: %w", err)
	}
	top, err := p.ImageIndex()
	if err != nil {
		return nil, err
	}
	im, err := top.IndexManifest()
	if err != nil {
		return nil, err
	}
	if len(im.Manifests) != 1 {
		return nil, fmt.Errorf("the image the builder sent names %d images; expected one", len(im.Manifests))
	}
	b := &built{dir: dir, root: im.Manifests[0]}
	switch b.root.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		b.index, err = top.ImageIndex(b.root.Digest)
	case types.OCIManifestSchema1, types.DockerManifestSchema2:
		b.image, err = top.Image(b.root.Digest)
	default:
		err = fmt.Errorf("the builder sent a %s, which is not an image", b.root.MediaType)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// platformImages lists the platforms' images, leaving out attestations.
func (b *built) platformImages() ([]PlatformImage, error) {
	if b.image != nil {
		size, err := imageSize(b.image)
		if err != nil {
			return nil, err
		}
		cf, err := b.image.ConfigFile()
		if err != nil {
			return nil, err
		}
		pl := cf.Platform()
		ps := ""
		if pl != nil {
			ps = pl.String()
		}
		return []PlatformImage{{Platform: ps, Digest: b.root.Digest.String(), Size: size}}, nil
	}
	im, err := b.index.IndexManifest()
	if err != nil {
		return nil, err
	}
	var out []PlatformImage
	for _, d := range im.Manifests {
		if d.Annotations[annoRefType] == attestation {
			continue
		}
		img, err := b.index.Image(d.Digest)
		if err != nil {
			return nil, err
		}
		size, err := imageSize(img)
		if err != nil {
			return nil, err
		}
		ps := ""
		if d.Platform != nil {
			ps = d.Platform.String()
		}
		out = append(out, PlatformImage{Platform: ps, Digest: d.Digest.String(), Size: size})
	}
	slices.SortFunc(out, func(a, b PlatformImage) int { return strings.Compare(a.Platform, b.Platform) })
	return out, nil
}

func imageSize(img v1.Image) (int64, error) {
	m, err := img.Manifest()
	if err != nil {
		return 0, err
	}
	raw, err := img.RawManifest()
	if err != nil {
		return 0, err
	}
	total := int64(len(raw)) + m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total, nil
}

// statement is an in-toto statement, as far as Launchway reads one.
type statement struct {
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Subject       []subject       `json:"subject"`
	Predicate     json.RawMessage `json:"predicate"`
}

type subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// builderProvenance returns the provenance the builder attached for each
// platform's image, as raw in-toto statements by platform.
func (b *built) builderProvenance() (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if b.index == nil {
		return out, nil
	}
	im, err := b.index.IndexManifest()
	if err != nil {
		return nil, err
	}
	platformOf := map[string]string{}
	for _, d := range im.Manifests {
		if d.Platform != nil {
			platformOf[d.Digest.String()] = d.Platform.String()
		}
	}
	for _, d := range im.Manifests {
		if d.Annotations[annoRefType] != attestation {
			continue
		}
		img, err := b.index.Image(d.Digest)
		if err != nil {
			return nil, err
		}
		m, err := img.Manifest()
		if err != nil {
			return nil, err
		}
		for _, l := range m.Layers {
			if !strings.HasPrefix(l.Annotations["in-toto.io/predicate-type"], "https://slsa.dev/provenance/") {
				continue
			}
			layer, err := img.LayerByDigest(l.Digest)
			if err != nil {
				return nil, err
			}
			rc, err := layer.Compressed()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(rc, 16<<20))
			rc.Close()
			if err != nil {
				return nil, err
			}
			out[platformOf[d.Annotations[annoRefDigest]]] = data
		}
	}
	return out, nil
}

// merge makes one image index of the images several builders made, each
// for its own platforms, with their attestations.
func merge(parts []*built) (v1.ImageIndex, error) {
	idx := mutate.IndexMediaType(empty.Index, types.OCIImageIndex)
	var adds []mutate.IndexAddendum
	for _, b := range parts {
		if b.image != nil {
			cf, err := b.image.ConfigFile()
			if err != nil {
				return nil, err
			}
			d := b.root
			d.Platform = cf.Platform()
			adds = append(adds, mutate.IndexAddendum{Add: b.image, Descriptor: d})
			continue
		}
		im, err := b.index.IndexManifest()
		if err != nil {
			return nil, err
		}
		for _, d := range im.Manifests {
			img, err := b.index.Image(d.Digest)
			if err != nil {
				return nil, err
			}
			adds = append(adds, mutate.IndexAddendum{Add: img, Descriptor: d})
		}
	}
	return mutate.AppendManifests(idx, adds...), nil
}
