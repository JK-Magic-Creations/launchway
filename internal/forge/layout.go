package forge

import (
	"strconv"
	"strings"
)

// RefLayout says where a forge publishes the fetchable head of each proposal
// as a ref: Prefix, the proposal's number, Suffix. Forges differ (one uses
// refs/pull/<n>/head, another refs/merge-requests/<n>/head, some publish
// none), so Launchway asks the provider instead of assuming one shape. The
// zero layout means the forge publishes no such refs.
type RefLayout struct {
	Prefix string `json:"prefix,omitempty"` // for example "refs/pull"
	Suffix string `json:"suffix,omitempty"` // for example "/head"
}

// PullRefLayout is the shape used by hosts that publish refs/pull/<n>/head.
// Plain Git assumes it, as Launchway always has; a host that does not publish
// such refs simply has none to find.
var PullRefLayout = RefLayout{Prefix: "refs/pull", Suffix: "/head"}

// IsZero reports whether the forge publishes no proposal refs.
func (l RefLayout) IsZero() bool { return l.Prefix == "" }

// Ref is the ref of proposal n.
func (l RefLayout) Ref(n int) string { return l.Prefix + "/" + strconv.Itoa(n) + l.Suffix }

// Parse reads a ref of this layout and returns the proposal number.
func (l RefLayout) Parse(ref string) (int, bool) {
	if l.IsZero() {
		return 0, false
	}
	rest, ok := strings.CutPrefix(ref, l.Prefix+"/")
	if !ok {
		return 0, false
	}
	num, ok := strings.CutSuffix(rest, l.Suffix)
	if !ok || num == "" || (num[0] == '0') {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 || strconv.Itoa(n) != num {
		return 0, false
	}
	return n, true
}
