package api

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/parity"
)

// The interface parity rule (doc 21): every API operation has an MCP tool
// and a CLI command that do the same, or a written reason why not. The
// table is internal/parity; cmd/launchway checks the commands.

func TestParityEveryRouteHasARow(t *testing.T) {
	var s Server
	routes := map[string]bool{}
	for _, r := range s.routes() {
		routes[r.pattern] = true
	}
	rows := map[string]parity.Row{}
	for _, r := range parity.Rows() {
		if _, dup := rows[r.Route]; dup {
			t.Errorf("internal/parity has two rows for %s", r.Route)
		}
		rows[r.Route] = r
		if !routes[r.Route] {
			t.Errorf("internal/parity has a row for %s, which is not a route", r.Route)
		}
	}
	for p := range routes {
		r, ok := rows[p]
		if !ok {
			t.Errorf("the route %s has no row in internal/parity: say which MCP tool and CLI command do the same (status equal), or why not (exempt, with the reason)", p)
			continue
		}
		switch r.Status {
		case parity.Equal:
			if r.MCP == "" || r.CLI == "" {
				t.Errorf("%s is marked equal but lacks an MCP tool or a CLI command", p)
			}
		case parity.Gap, parity.Exempt:
			if len(strings.Fields(r.Reason)) < 4 {
				t.Errorf("%s is a %s without a written reason", p, r.Status)
			}
		default:
			t.Errorf("%s has the status %q", p, r.Status)
		}
	}
}

func TestParityMCPToolsExist(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "all"), nil, nil)
	have := append(toolNames(t, cs), agentToolNames(t, e)...)
	used := map[string]bool{}
	for _, r := range parity.Rows() {
		for _, tool := range r.Tools() {
			used[tool] = true
			if !slices.Contains(have, tool) {
				t.Errorf("%s names the MCP tool %s, which does not exist", r.Route, tool)
			}
		}
	}
	// A tool that changes configuration must be part of the table, so no
	// capability exists only in MCP.
	for _, name := range have {
		if !used[name] && !mcpOnly[name] {
			t.Errorf("the MCP tool %s is in no row of internal/parity; add it to the API operation it calls, or to mcpOnly with a reason", name)
		}
	}
}

// mcpOnly are tools that no single API operation stands behind.
var mcpOnly = map[string]bool{
	"read_artifact_file":     true, // an artifact file is also a download the table lists as a gap
	"wait_for_run":           true, // a wait loop over the run's events
	"get_pipeline_reference": true, // the pipeline reference text; the docs site and the CLI help carry the same
}

func TestParityGapsOnlyShrink(t *testing.T) {
	if n := parity.Gaps(); n > parity.MaxGaps {
		t.Errorf("%d gaps recorded, at most %d allowed: a new operation needs its MCP tool and CLI command, not a new gap", n, parity.MaxGaps)
	} else if n < parity.MaxGaps {
		t.Errorf("%d gaps recorded but MaxGaps is %d: lower MaxGaps in internal/parity/parity.go to lock in the progress", n, parity.MaxGaps)
	}
}

// docs/reference/interface-parity.md is the table; LAUNCHWAY_UPDATE_DOCS=1
// rewrites it.
func TestParityReferencePageIsCurrent(t *testing.T) {
	const path = "../../docs/reference/interface-parity.md"
	want := parity.Markdown()
	if os.Getenv("LAUNCHWAY_UPDATE_DOCS") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("docs/reference/interface-parity.md is not the table: run LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/api -run TestParityReferencePageIsCurrent")
	}
}
