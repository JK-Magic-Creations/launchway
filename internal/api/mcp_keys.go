package api

// The MCP tools for Git over SSH and protected branches: the server's SSH
// address and host key fingerprints, a person's keys, a repository's deploy
// keys, and the protected branches. Each calls the same route as the API, so
// the answer for a caller is the API's (someone else's key does not exist; a
// repository one may not administer refuses deploy keys).

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addKeyTools(srv *mcp.Server, c *mcpCall) {
	addUserTools(srv, c)
	addHookTools(srv, c)
	addDebriefTools(srv, c)
	addPullTools(srv, c)
	addTool(srv, c, &mcp.Tool{Name: "get_ssh_info", Annotations: readOnly("Get the SSH settings of Git hosting"),
		Description: "Whether Git over SSH is on, the host and port clone addresses use, and the server's host key fingerprints (SHA256), which people compare the first time they connect."},
		mcpCallTimeout, getSSHInfo)
	addTool(srv, c, &mcp.Tool{Name: "list_ssh_keys", Annotations: readOnly("List your SSH keys"),
		Description: "Your own SSH public keys: name, type, SHA256 fingerprint, expiry, when and from where each was last used."},
		mcpCallTimeout, listSSHKeys)
	addTool(srv, c, &mcp.Tool{Name: "add_ssh_key", Annotations: changes("Add an SSH key", false, false),
		Description: "Add a public key (ed25519, ECDSA, or RSA of at least 3072 bits; one line from a .pub file) for yourself, with a name and an optional expiry (RFC 3339). A key can belong to one owner only. Audited."},
		mcpCallTimeout, addSSHKey)
	addTool(srv, c, &mcp.Tool{Name: "delete_ssh_key", Annotations: changes("Delete an SSH key", true, true),
		Description: "Delete one of your SSH keys by ID; it stops working at once. Audited."},
		mcpCallTimeout, deleteSSHKey)
	addTool(srv, c, &mcp.Tool{Name: "list_deploy_keys", Annotations: readOnly("List the deploy keys of a repository"),
		Description: "The deploy keys of one repository (its administrators only): name, fingerprint, whether the key may push, expiry, last use."},
		mcpCallTimeout, listDeployKeys)
	addTool(srv, c, &mcp.Tool{Name: "list_notifications", Annotations: readOnly("List notifications"),
		Description: "The log of notifications Launchway sent or tried to send (an approval waiting, a failed Sortie or deployment, a rollback): channel, event, status (sent, pending a retry, or failed), attempts and why one failed. Never an address or key of a channel."},
		mcpCallTimeout, listNotifications)
	addTool(srv, c, &mcp.Tool{Name: "get_protected_branches", Annotations: readOnly("Get the protected branches of a repository"),
		Description: "The branch names and patterns (such as main or release/*) that take no direct push except from the repository's administrators and cannot be deleted."},
		mcpCallTimeout, getProtectedBranches)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "add_deploy_key", Annotations: changes("Add a deploy key to a repository", false, false),
			Description: "Repository administrators only. Add a public key that opens this one repository over SSH: read-only unless write is true. Audited."},
			mcpCallTimeout, addDeployKey)
		addTool(srv, c, &mcp.Tool{Name: "delete_deploy_key", Annotations: changes("Delete a deploy key", true, true),
			Description: "Repository administrators only. Delete a deploy key by ID; it stops working at once. Audited."},
			mcpCallTimeout, deleteDeployKey)
		addTool(srv, c, &mcp.Tool{Name: "set_protected_branches", Annotations: changes("Set the protected branches of a repository", false, true),
			Description: "Repository administrators only. Replace the repository's protected branch patterns (an empty list removes the protection). Audited."},
			mcpCallTimeout, setProtectedBranches)
	}
}

type sshInfoOut struct {
	Enabled      bool   `json:"enabled"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
	Certificates bool   `json:"certificates"`
	HostKeys     []struct {
		Type        string `json:"type"`
		Fingerprint string `json:"fingerprint"`
	} `json:"host_keys"`
}

func getSSHInfo(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (sshInfoOut, error) {
	var out sshInfoOut
	err := c.apiJSON(ctx, "get_ssh_info", http.MethodGet, "/api/v1/ssh", nil, &out)
	return out, err
}

type keyOut struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Fingerprint  string `json:"fingerprint"`
	CanWrite     bool   `json:"can_write,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	LastUsedFrom string `json:"last_used_from,omitempty"`
}

type keysOut struct {
	Summary string   `json:"summary"`
	Keys    []keyOut `json:"keys"`
}

func keysView(in []keyOut) keysOut {
	for i := range in {
		in[i].Name = untrusted(in[i].Name, 100)
	}
	return keysOut{Summary: pluralN(len(in), "key"), Keys: in}
}

func pluralN(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func listSSHKeys(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (keysOut, error) {
	var page struct {
		Items []keyOut `json:"items"`
	}
	if err := c.apiJSON(ctx, "list_ssh_keys", http.MethodGet, "/api/v1/user/ssh-keys", nil, &page); err != nil {
		return keysOut{}, err
	}
	return keysView(page.Items), nil
}

type addKeyIn struct {
	Name      string `json:"name,omitempty" jsonschema:"a name for the key, such as the computer it is on (default: the key's comment)"`
	PublicKey string `json:"public_key" jsonschema:"the public key, one line from a .pub file"`
	ExpiresAt string `json:"expires_at,omitempty" jsonschema:"optional expiry, RFC 3339, such as 2027-06-30T00:00:00Z"`
}

func addSSHKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addKeyIn) (keyOut, error) {
	var out keyOut
	err := c.apiJSON(ctx, "add_ssh_key", http.MethodPost, "/api/v1/user/ssh-keys", map[string]any{"name": in.Name, "public_key": in.PublicKey, "expires_at": in.ExpiresAt}, &out)
	out.Name = untrusted(out.Name, 100)
	return out, err
}

type keyIDIn struct {
	ID string `json:"id" jsonschema:"the key's ID, from the list"`
}

type deletedKeyOut struct {
	Deleted string `json:"deleted"`
}

func deleteSSHKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in keyIDIn) (deletedKeyOut, error) {
	if in.ID == "" {
		return deletedKeyOut{}, errors.New("say which key: its id")
	}
	if err := c.apiJSON(ctx, "delete_ssh_key", http.MethodDelete, apiPath("/api/v1/user/ssh-keys/%s", in.ID), nil, nil); err != nil {
		return deletedKeyOut{}, err
	}
	return deletedKeyOut{Deleted: in.ID}, nil
}

func listDeployKeys(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repositoryIn) (keysOut, error) {
	if in.Owner == "" || in.Name == "" {
		return keysOut{}, errors.New("say which repository: owner and name")
	}
	var page struct {
		Items []keyOut `json:"items"`
	}
	if err := c.apiJSON(ctx, "list_deploy_keys", http.MethodGet, apiPath("/api/v1/repositories/%s/%s/deploy-keys", in.Owner, in.Name), nil, &page); err != nil {
		return keysOut{}, err
	}
	return keysView(page.Items), nil
}

type addDeployKeyIn struct {
	Owner     string `json:"owner" jsonschema:"the repository's owner"`
	Name      string `json:"name" jsonschema:"the repository's name"`
	KeyName   string `json:"key_name,omitempty" jsonschema:"a name for the key (default: the key's comment)"`
	PublicKey string `json:"public_key" jsonschema:"the public key, one line from a .pub file"`
	Write     bool   `json:"write,omitempty" jsonschema:"let the key push (default: read-only)"`
	ExpiresAt string `json:"expires_at,omitempty" jsonschema:"optional expiry, RFC 3339"`
}

func addDeployKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addDeployKeyIn) (keyOut, error) {
	if in.Owner == "" || in.Name == "" {
		return keyOut{}, errors.New("say which repository: owner and name")
	}
	var out keyOut
	err := c.apiJSON(ctx, "add_deploy_key", http.MethodPost, apiPath("/api/v1/repositories/%s/%s/deploy-keys", in.Owner, in.Name),
		map[string]any{"name": in.KeyName, "public_key": in.PublicKey, "write": in.Write, "expires_at": in.ExpiresAt}, &out)
	out.Name = untrusted(out.Name, 100)
	return out, err
}

type deployKeyIDIn struct {
	Owner string `json:"owner" jsonschema:"the repository's owner"`
	Name  string `json:"name" jsonschema:"the repository's name"`
	ID    string `json:"id" jsonschema:"the deploy key's ID, from the list"`
}

func deleteDeployKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in deployKeyIDIn) (deletedKeyOut, error) {
	if in.Owner == "" || in.Name == "" || in.ID == "" {
		return deletedKeyOut{}, errors.New("say which deploy key: owner, name and id")
	}
	if err := c.apiJSON(ctx, "delete_deploy_key", http.MethodDelete, apiPath("/api/v1/repositories/%s/%s/deploy-keys/%s", in.Owner, in.Name, in.ID), nil, nil); err != nil {
		return deletedKeyOut{}, err
	}
	return deletedKeyOut{Deleted: in.ID}, nil
}

type protectedOut struct {
	Patterns        []string         `json:"patterns"`
	PipelineWriters []pipelineWriter `json:"pipeline_writers"`
}

// pipelineWriter admits a pipeline to a protected branch, for exactly some files.
type pipelineWriter struct {
	Branch   string   `json:"branch" jsonschema:"the protected branch or pattern the rule covers, such as main"`
	Projects []string `json:"projects" jsonschema:"project names or patterns whose deploy steps are admitted, such as mcp-*"`
	Files    []string `json:"files" jsonschema:"the files the pipeline may change, relative to the repository's root; ** matches any number of directories"`
}

func getProtectedBranches(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repositoryIn) (protectedOut, error) {
	if in.Owner == "" || in.Name == "" {
		return protectedOut{}, errors.New("say which repository: owner and name")
	}
	var out protectedOut
	err := c.apiJSON(ctx, "get_protected_branches", http.MethodGet, apiPath("/api/v1/repositories/%s/%s/protected-branches", in.Owner, in.Name), nil, &out)
	return out, err
}

type setProtectedIn struct {
	Owner    string   `json:"owner" jsonschema:"the repository's owner"`
	Name     string   `json:"name" jsonschema:"the repository's name"`
	Patterns []string `json:"patterns" jsonschema:"branch names or patterns such as main or release/*; an empty list removes the protection"`
	// PipelineWriters replaces the rules that admit pipelines (deploy steps that write back to this repository); leave it out to keep them.
	PipelineWriters *[]pipelineWriter `json:"pipeline_writers,omitempty" jsonschema:"rules that admit a deploy step's own credential to a protected branch for exactly the files it may write; leave out to keep the current rules, give an empty list to remove them"`
}

func setProtectedBranches(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setProtectedIn) (protectedOut, error) {
	if in.Owner == "" || in.Name == "" {
		return protectedOut{}, errors.New("say which repository: owner and name")
	}
	var out protectedOut
	err := c.apiJSON(ctx, "set_protected_branches", http.MethodPut, apiPath("/api/v1/repositories/%s/%s/protected-branches", in.Owner, in.Name), map[string]any{"patterns": in.Patterns, "pipeline_writers": in.PipelineWriters}, &out)
	return out, err
}
