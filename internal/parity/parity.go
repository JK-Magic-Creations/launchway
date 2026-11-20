// Package parity is the table behind the interface parity rule: every
// capability Launchway has is available through the HTTP API, the MCP
// server, the command line and (for configuration) Terraform, with the same
// permissions and the same checks. The table names, for each API operation,
// the MCP tool and the CLI command that do the same, or says why there is
// none.
//
// Tests in internal/api and cmd/launchway fail when a route has no row, when
// a row names a tool or command that does not exist, and when the number
// of recorded gaps grows. A gap is a debt, written down so it cannot grow
// quietly; an exemption is a design decision with its reason.
package parity

import (
	"sort"
	"strings"
)

// Status says how a row stands.
type Status string

const (
	// Equal: the operation is available in every interface that applies.
	Equal Status = "equal"
	// Gap: an interface lacks it today; Reason says which and why.
	Gap Status = "gap"
	// Exempt: it is not an operation for people or agents (the runner
	// protocol, sign-in), or another interface cannot sensibly have it.
	Exempt Status = "exempt"
)

// MaxGaps is the number of gaps recorded. It may only go down: closing a gap
// lowers it, and a new operation must come with its tool and command, not a
// new gap.
const MaxGaps = 50

// Row is one API operation.
type Row struct {
	// Route is the method and path pattern, as in the route table.
	Route string
	// MCP lists the MCP tools that do it, separated by commas.
	MCP string
	// CLI lists the command lines that do it, separated by commas.
	CLI string
	// Terraform is the planned resource or data source, for configuration.
	Terraform string
	Status    Status
	Reason    string
}

// Rows returns the table, sorted by route.
func Rows() []Row {
	out := append([]Row(nil), rows...)
	sort.Slice(out, func(i, j int) bool {
		return pathOf(out[i].Route) < pathOf(out[j].Route) || pathOf(out[i].Route) == pathOf(out[j].Route) && out[i].Route < out[j].Route
	})
	return out
}

func pathOf(route string) string {
	_, p, _ := strings.Cut(route, " ")
	return p
}

// Tools returns the names of the MCP tools a row names (the words that
// start each comma-separated entry; "list_config (kind runner-pools)" is
// the tool list_config).
func (r Row) Tools() []string { return names(r.MCP) }

// Commands returns the command lines a row names.
func (r Row) Commands() []string {
	var out []string
	for _, c := range strings.Split(r.CLI, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func names(list string) []string {
	var out []string
	for _, c := range strings.Split(list, ",") {
		c = strings.TrimSpace(c)
		if i := strings.IndexByte(c, ' '); i > 0 {
			c = c[:i]
		}
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// Gaps counts the rows that are gaps.
func Gaps() int {
	n := 0
	for _, r := range rows {
		if r.Status == Gap {
			n++
		}
	}
	return n
}

// Markdown is the table as the reference page shows it.
func Markdown() string {
	var b strings.Builder
	b.WriteString("---\ntitle: Interface parity\n---\n\n# Interface parity\n\n")
	b.WriteString("Everything Launchway can do is available through the HTTP API, the MCP server and the command line, with the same permissions and the same checks, and every kind of configuration is planned as a Terraform resource. This page is generated from the table the tests check (`internal/parity`): a route in the API without a row, a row naming an MCP tool or command that does not exist, or a growing number of gaps fails the build.\n\n")
	b.WriteString("A gap is something an interface lacks today; the number may only go down. An exemption is an operation that is not for people or agents, with the reason.\n\n")
	b.WriteString("| Operation | MCP tool | CLI command | Terraform | Status |\n|---|---|---|---|---|\n")
	for _, r := range Rows() {
		st := string(r.Status)
		if r.Reason != "" {
			st += ": " + r.Reason
		}
		cell := func(s string) string {
			if s == "" {
				return "—"
			}
			return "`" + strings.ReplaceAll(s, ", ", "`, `") + "`"
		}
		b.WriteString("| `" + r.Route + "` | " + cell(r.MCP) + " | " + cell(r.CLI) + " | " + cell(r.Terraform) + " | " + strings.ReplaceAll(st, "|", "/") + " |\n")
	}
	return b.String()
}
