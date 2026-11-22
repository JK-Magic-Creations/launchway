package api

import (
	"slices"
	"strings"
	"testing"
)

// newAgentToken registers an agent for pay-api as ada and returns its token.
func (e *authEnv) newAgentToken(t *testing.T, name string) string {
	t.Helper()
	r := e.signIn(t, "ada").do("POST", e.srv.URL+"/api/v1/work/agents", `{"name":"`+name+`","scope":{"kind":"project","id":"pay-api"},"projects":["pay-api"]}`)
	var out struct{ Token string }
	r.json(t, &out)
	if out.Token == "" {
		t.Fatalf("registering an agent: %d %s", r.status, r.body)
	}
	return out.Token
}

// agentToolNames are the tools an agent's token is offered.
func agentToolNames(t *testing.T, e *authEnv) []string {
	t.Helper()
	return toolNames(t, mcpSession(t, e.mcpURL(), e.newAgentToken(t, "lister"), nil, nil))
}

var (
	agentOnlyTools   = []string{"get_agent_identity", "get_work_queue", "claim_next_work", "heartbeat_work", "hand_back_work", "request_work_review", "report_work_usage", "review_pull_request_as_agent"}
	agentSharedTools = []string{"get_work_item", "update_work_item", "comment_on_work_item", "link_work_code"}
)

// An agent's token is offered its own calls and four shared tools, and no
// person tool; a person is offered none of the agent calls.
func TestMCPWorkToolsForAgentsAndPeople(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	agent := toolNames(t, mcpSession(t, e.mcpURL(), e.newAgentToken(t, "scout"), nil, nil))
	for _, want := range append(slices.Clone(agentOnlyTools), agentSharedTools...) {
		if !slices.Contains(agent, want) {
			t.Errorf("an agent is not offered %s", want)
		}
	}
	for _, n := range agent {
		if strings.Contains(n, "work") && !slices.Contains(agentOnlyTools, n) && !slices.Contains(agentSharedTools, n) {
			t.Errorf("an agent is offered the person tool %s", n)
		}
		if n == "create_work_agent" || n == "set_work_settings" || n == "update_work_agent" {
			t.Errorf("an agent is offered %s", n)
		}
	}
	for _, who := range []string{"ada", "otto"} {
		for _, n := range toolNames(t, mcpSession(t, e.mcpURL(), e.tokenFor(t, who, "w"), nil, nil)) {
			if slices.Contains(agentOnlyTools, n) {
				t.Errorf("%s is offered the agent tool %s", who, n)
			}
		}
	}
}

// A person makes an item and an agent, the agent claims it and works it
// through its own tools, and its answers carry text clipped as untrusted.
func TestMCPWorkRoundTrip(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	ada := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "w"), nil, nil)
	item := mustCall(t, ada, "create_work_item", map[string]any{"scope": "project:pay-api", "title": "Fix the bell\x07", "body": "ring it", "assignees": []string{}})
	ref := str(item, "ref")
	if ref == "" {
		t.Fatalf("created: %v", item)
	}
	got := mustCall(t, ada, "get_work_item", map[string]any{"item": ref})
	if str(got, "title") != "Fix the bell" {
		t.Errorf("title: %q", str(got, "title"))
	}
	mustCall(t, ada, "comment_on_work_item", map[string]any{"item": ref, "body": "first"})
	if n := len(list(mustCall(t, ada, "get_work_item", map[string]any{"item": ref}), "comments")); n != 1 {
		t.Errorf("comments: %d", n)
	}
	mustCall(t, ada, "update_work_item", map[string]any{"item": ref, "priority": "high", "add_labels": []string{"agent:ok"}})
	if str(mustCall(t, ada, "get_work_item", map[string]any{"item": ref}), "priority") != "high" {
		t.Error("the change was not made")
	}
	if page := mustCall(t, ada, "list_work_items", map[string]any{"scope": "project:pay-api", "query": "bell"}); len(list(page, "items")) != 1 {
		t.Errorf("search: %v", page)
	}
	if _, errText := call(t, ada, "get_work_item", map[string]any{}); errText == "" {
		t.Errorf("no item: %q", errText)
	}

	agent := mcpSession(t, e.mcpURL(), e.newAgentToken(t, "scout2"), nil, nil)
	me := mustCall(t, agent, "get_agent_identity", nil)
	if str(me, "name") != "scout2" {
		t.Errorf("identity: %v", me)
	}
	if _, errText := call(t, agent, "hand_back_work", map[string]any{"item": ref}); errText == "" {
		t.Error("handing back with no claim and no reason worked")
	}
	if _, errText := call(t, agent, "create_work_item", map[string]any{"scope": "project:pay-api", "title": "x"}); errText == "" {
		t.Error("an agent used a person tool")
	}
}
