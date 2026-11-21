package api

// MCP tools that bring work in: a list of stories, or a repository's archived
// issues made into live items. See mcp_work.go.

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type importStoriesIn struct {
	scopeIn
	Items   []storyIn `json:"items" jsonschema:"the stories: key, epic (the key of its epic), title, body (with an 'Acceptance criteria' checklist), labels, milestone, depends_on (keys), rank, phase, doc"`
	Preview bool      `json:"preview,omitempty" jsonschema:"say what would be made without making it"`
	Update  bool      `json:"update,omitempty" jsonschema:"change the items a key already made, instead of leaving them"`
}

type promoteIn struct {
	Repository string `json:"repository" jsonschema:"owner/name of the repository whose archived issues become items"`
	Preview    bool   `json:"preview,omitempty" jsonschema:"say what would be made without making it"`
}

func addWorkImportTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "import_work_stories", Annotations: changes("Import stories as work items", false, true),
		Description: "Make work items from a list of stories (epics and their stories, with keys, so running it again finds what it made). preview reports what would happen first. Audited."},
		mcpCallTimeout, workSend("import_work_stories", http.MethodPost, fixed[importStoriesIn]("/api/v1/work/import/stories"), whole[importStoriesIn]))
	addTool(srv, c, &mcp.Tool{Name: "promote_archive", Annotations: changes("Make a repository's archived issues live work items", false, true),
		Description: "Turn the issues in a repository's read-only archive (see import_archive) into work items of that project, keeping numbers, comments and states; running it again finds what it made. preview reports first. Audited."},
		mcpCallTimeout, workSend("promote_archive", http.MethodPost, fixed[promoteIn]("/api/v1/work/import/archive"), whole[promoteIn]))
}

type storyIn struct {
	Key       string   `json:"key" jsonschema:"a name for the story that stays the same when you run the import again"`
	Epic      string   `json:"epic,omitempty" jsonschema:"the key of its epic"`
	Title     string   `json:"title" jsonschema:"a title"`
	Body      string   `json:"body,omitempty" jsonschema:"the description, Markdown"`
	Labels    []string `json:"labels,omitempty" jsonschema:"the labels, such as bug or agent:docs"`
	Milestone string   `json:"milestone,omitempty" jsonschema:"a milestone's title"`
	DependsOn []string `json:"depends_on,omitempty" jsonschema:"keys of the stories it needs first"`
	Rank      float64  `json:"rank,omitempty"`
	Phase     string   `json:"phase,omitempty"`
	Doc       string   `json:"doc,omitempty"`
}
