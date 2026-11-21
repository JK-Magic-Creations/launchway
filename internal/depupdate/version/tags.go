package version

import (
	"strconv"
	"strings"
)

// Tag is a container image tag split into its numeric version and the rest.
// "1.22.3-alpine3.20" is numbers [1 22 3] and variant "-alpine3.20"; a tag
// with a leading "v" keeps that as Prefix. Tags that start with no number
// ("latest", "bookworm") are not comparable.
type Tag struct {
	Raw     string
	Prefix  string
	Nums    []uint64
	Variant string
	Pre     bool // the variant says alpha, beta, rc, dev, snapshot, nightly, edge
	OK      bool
}

var preWords = []string{"alpha", "beta", "rc", "dev", "snapshot", "nightly", "edge", "preview", "canary", "test"}

// ParseTag reads an image tag.
func ParseTag(s string) Tag {
	t := Tag{Raw: s}
	rest := s
	if strings.HasPrefix(rest, "v") && len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9' {
		t.Prefix, rest = "v", rest[1:]
	}
	i := 0
	for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.') {
		i++
	}
	num := strings.TrimRight(rest[:i], ".")
	if num == "" {
		return t
	}
	// "3.19" followed by "-alpine" is fine; "3.19.1.5" has four parts.
	for _, p := range strings.Split(num, ".") {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Tag{Raw: s}
		}
		t.Nums = append(t.Nums, n)
	}
	t.Variant = rest[len(num):]
	low := strings.ToLower(t.Variant)
	for _, w := range preWords {
		if strings.Contains(low, w) {
			t.Pre = true
		}
	}
	t.OK = true
	return t
}

// SameFamily reports whether two tags are the same kind of tag (the same
// prefix, the same variant and the same number of version parts), so that
// "1.22-alpine" is compared with "1.23-alpine" but not "1.23-bookworm".
func (t Tag) SameFamily(o Tag) bool {
	return t.OK && o.OK && t.Prefix == o.Prefix && t.Variant == o.Variant && len(t.Nums) == len(o.Nums)
}

// Compare orders two tags of one family.
func (t Tag) Compare(o Tag) int {
	for i := range t.Nums {
		if i >= len(o.Nums) {
			break
		}
		if t.Nums[i] != o.Nums[i] {
			return cmpU(t.Nums[i], o.Nums[i])
		}
	}
	return 0
}

// KindOfTag says how big the step from one tag to another is: the first
// number that changed (a tag with one number is always "major").
func KindOfTag(from, to Tag) Kind {
	if to.Compare(from) <= 0 {
		return KindNone
	}
	for i := range from.Nums {
		if from.Nums[i] != to.Nums[i] {
			switch i {
			case 0:
				return KindMajor
			case 1:
				return KindMinor
			}
			return KindPatch
		}
	}
	return KindNone
}
