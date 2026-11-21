package executor

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

// ImageIdentity says which bytes a step's image is. A reference such as
// golang:1.26 names whatever the registry holds under that tag today, so a
// cache keyed on the reference alone would hand a build made in one JDK,
// Node or Python an entry made in another. ID is what the reference
// resolved to: a content digest (sha256:...) or, for an engine that only
// knows its own copy, the image id. Empty when it could not be resolved,
// in which case the key can only use the reference and Source says so.
type ImageIdentity struct {
	Ref    string
	ID     string
	Source string // where ID came from: "digest in the reference", "container engine", "registry", or "" if unresolved
}

// VersionValue is what one of the author's version inputs came out as: the
// text of the command or the name of the file, and the output or the hash.
type VersionValue struct {
	Label string // "command: java -version", "file: .nvmrc"
	Value string
}

// KeyPart is one input of a cache key, in words, for the run page and for
// `launchway runs show`.
type KeyPart struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CacheKeyInput is everything a cache's key is made of.
type CacheKeyInput struct {
	// Platform is the runner's operating system and architecture.
	Platform string
	Name     string
	Text     string
	// Image is the image the step runs in, resolved.
	Image ImageIdentity
	// Versions are the commands' outputs and files' hashes the author named
	// under key.versions, in the order written.
	Versions []VersionValue
	// Files are those Stage returns for the key's globs.
	Files []StagedFile
}

// CacheKey works out the key of a cache and says what it was made of: the
// format, the platform, the name, the step's image resolved to its digest,
// the literal text, the author's version inputs, and the path and SHA-256 of
// each key file in sorted order. The same parts, in the same order, are what
// the run page shows.
func CacheKey(in CacheKeyInput) (string, []KeyPart) {
	h := sha256.New()
	var parts []KeyPart
	put := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	add := func(label, value, shown string) {
		put(label)
		put(value)
		parts = append(parts, KeyPart{Label: label, Value: shown})
	}
	add("format", CacheFormat, CacheFormat)
	add("platform", in.Platform, in.Platform)
	add("name", in.Name, in.Name)
	// The reference is hashed as well as what it resolved to, so that an
	// unresolved image still tells two references apart.
	img := in.Image.ID
	if img == "" {
		img = "(not resolved)"
	}
	add("image", in.Image.Ref+"\x00"+in.Image.ID, in.Image.Ref+" = "+img)
	put("text")
	put(in.Text)
	if in.Text != "" {
		parts = append(parts, KeyPart{Label: "text", Value: in.Text})
	}
	for _, v := range in.Versions {
		add("version "+v.Label, v.Value, shortLine(v.Value))
	}
	sorted := slices.Clone(in.Files)
	slices.SortFunc(sorted, func(a, b StagedFile) int { return strings.Compare(a.Path, b.Path) })
	put(strconv.Itoa(len(sorted)))
	for i, f := range sorted {
		put(f.Path)
		put(f.SHA256)
		if i < maxKeyPartFiles {
			parts = append(parts, KeyPart{Label: "file " + f.Path, Value: shortSum(f.SHA256)})
		}
	}
	if len(sorted) > maxKeyPartFiles {
		parts = append(parts, KeyPart{Label: "files", Value: "and " + strconv.Itoa(len(sorted)-maxKeyPartFiles) + " more"})
	}
	return hex.EncodeToString(h.Sum(nil)), parts
}

// maxKeyPartFiles is how many key files are listed in the parts; all of
// them are in the key.
const maxKeyPartFiles = 20

func shortSum(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// shortLine is a version output on one line, clipped.
func shortLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
