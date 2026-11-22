package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// containerName gives a template's container a name in the run:
// <uses-step>-<name> if that is a valid name nobody has, else a name made
// unique with a hash of where it came from.
func (x *expander) containerName(prefix, name string) string {
	base := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(prefix, "/"), "/", "-"), "_", "-") + "-" + name
	ok := func(n string) bool {
		return containerRe.MatchString(n) && !reservedNames[n] && !strings.HasPrefix(n, "image-") && !x.containerNames[n]
	}
	if !ok(base) {
		sum := sha256.Sum256([]byte(prefix + name))
		tail := name
		if len(tail) > 28 {
			tail = strings.TrimRight(tail[:28], "-")
		}
		base = "t" + hex.EncodeToString(sum[:])[:8] + "-" + tail
		for i := 2; !ok(base); i++ {
			base = fmt.Sprintf("t%s%d-%s", hex.EncodeToString(sum[:])[:6], i, tail)
		}
	}
	x.containerNames[base] = true
	return base
}

// mergeEach combines the loop values around a step with its own; its own
// win. It is nil when there are none.
func mergeEach(outer, own map[string]string) map[string]string {
	if len(outer) == 0 {
		return own
	}
	out := make(map[string]string, len(outer)+len(own))
	for k, v := range outer {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}
