package api

import (
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// decisionOut is the answer to approving or rejecting: ok, and how the
// approval stands (schema 27): who has approved, how many are needed, and
// whether it is decided.
type decisionOut struct {
	OK bool `json:"ok"`
	*engine.Decision
}

// progressText says how an approval stands, such as "1 of 2 approved (ann)".
func progressText(d *engine.Decision) string {
	if d == nil || d.Needed < 1 {
		return ""
	}
	names := make([]string, len(d.Approvals))
	for i, a := range d.Approvals {
		names[i] = a.Actor
	}
	text := fmt.Sprintf("%d of %d approved", len(d.Approvals), d.Needed)
	if len(names) > 0 {
		text += " (" + strings.Join(names, ", ") + ")"
	}
	if d.NeededFrom != "" {
		text += "; the number comes from " + d.NeededFrom
	}
	return text
}

// approvalNames are the names of the people who approved.
func approvalNames(list []engine.ApprovalRecord) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Actor
	}
	return out
}
