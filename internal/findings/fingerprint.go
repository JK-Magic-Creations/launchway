package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// placeholderFP is a partialFingerprints value some tools write instead of
// a real one (semgrep's "matchBasedId/v1" without a login).
const placeholderFP = "requires login"

// fingerprints sets Fingerprint on every kept finding; see the package
// comment for the recipe. It runs once the whole file has been read, since
// a partialFingerprints value is a placeholder when every result in the
// file shares it. Values from result.fingerprints (full fingerprints, which
// semgrep writes) are used the same way, under keys starting
// "fingerprints/".
//
// A line-based fingerprint (start line and message) changes when the code
// moves up or down the file; a partial fingerprint such as CodeQL's
// primaryLocationLineHash does not.
func (p *parser) fingerprints() {
	total := p.rep.Counts.Total
	placeholder := func(k, v string) bool {
		if toLowerTrim(v) == placeholderFP {
			return true
		}
		st := p.fpStats[k]
		return st != nil && total >= 2 && !st.differ && st.count == total
	}
	seen := map[string]int{}
	for i := range p.rep.Findings {
		f := &p.rep.Findings[i]
		var pairs []string
		for _, kv := range p.kept[i].partial {
			if !placeholder(kv.k, kv.v) {
				pairs = append(pairs, kv.k+"="+kv.v)
			}
		}
		sort.Strings(pairs)
		parts := []string{strings.ToLower(f.Tool), f.RuleID, f.File}
		if len(pairs) > 0 {
			parts = append(parts, "partial")
			parts = append(parts, pairs...)
		} else {
			parts = append(parts, "line", strconv.Itoa(f.StartLine), f.Message)
		}
		sum := fingerprint(parts)
		seen[sum]++
		if n := seen[sum]; n > 1 {
			sum = fingerprint(append(parts, "#"+strconv.Itoa(n)))
		}
		f.Fingerprint = sum
		p.kept[i] = kept{}
	}
}

// fingerprint hashes NUL-separated parts and returns the first 16 bytes of
// the SHA-256 in hex.
func fingerprint(parts []string) string {
	h := sha256.New()
	for i, s := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(s))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:16])
}
