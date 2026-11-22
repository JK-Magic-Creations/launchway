package api

// The MCP tools for verified commits and tags: a person's signing keys, the
// verdict on commits and tags, and the installation's own signing key. Each
// calls the same route as the API.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addSigningTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_signing_keys", Annotations: readOnly("List your signing keys"),
		Description: "Your own SSH signing keys, the ones commits and tags are signed with: name, fingerprint, expiry, and when one was revoked."},
		mcpCallTimeout, listSigningKeys)
	addTool(srv, c, &mcp.Tool{Name: "add_signing_key", Annotations: changes("Add a signing key", false, false),
		Description: "Register an SSH public key (one line from a .pub file) that you sign commits and tags with, so that what it signs shows as verified. A key can belong to one person only. Audited."},
		mcpCallTimeout, addSigningKey)
	addTool(srv, c, &mcp.Tool{Name: "revoke_signing_key", Annotations: changes("Revoke a signing key", true, true),
		Description: "Revoke one of your signing keys from now on: commits dated after that show as signed by a revoked key; earlier ones stay verified. Audited."},
		mcpCallTimeout, revokeSigningKey)
	addTool(srv, c, &mcp.Tool{Name: "delete_signing_key", Annotations: changes("Delete a signing key", true, true),
		Description: "Delete one of your signing keys: what it signed shows as signed by an unknown key. Audited."},
		mcpCallTimeout, deleteSigningKey)
	addTool(srv, c, &mcp.Tool{Name: "verify_signatures", Annotations: readOnly("Check the signatures of commits and tags"),
		Description: "For commits and annotated tags of a hosted repository (by full object name, up to 200): verified, signed by an unknown key, bad signature, unsigned, key revoked or expired, or a format Launchway cannot check yet, with a plain explanation and whether the signer is the author or committer."},
		mcpCallTimeout, verifySignatures)
	addTool(srv, c, &mcp.Tool{Name: "get_signing_installation_key", Annotations: readOnly("Get the installation's signing key"),
		Description: "The public key with which Launchway signs the merge and suggestion commits it makes, its fingerprint, and the address it is published at. Add it to an allowed-signers file to verify those commits with Git."},
		mcpCallTimeout, getInstallationKey)
}

type signingKeyOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
}

type signingKeysOut struct {
	Summary string          `json:"summary"`
	Keys    []signingKeyOut `json:"keys"`
}

func listSigningKeys(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (signingKeysOut, error) {
	var page struct {
		Items []signingKeyOut `json:"items"`
	}
	if err := c.apiJSON(ctx, "list_signing_keys", http.MethodGet, "/api/v1/user/signing-keys", nil, &page); err != nil {
		return signingKeysOut{}, err
	}
	for i := range page.Items {
		page.Items[i].Name = untrusted(page.Items[i].Name, 100)
	}
	return signingKeysOut{Summary: pluralN(len(page.Items), "signing key"), Keys: page.Items}, nil
}

func addSigningKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addKeyIn) (signingKeyOut, error) {
	var out signingKeyOut
	err := c.apiJSON(ctx, "add_signing_key", http.MethodPost, "/api/v1/user/signing-keys", map[string]any{"name": in.Name, "public_key": in.PublicKey, "expires_at": in.ExpiresAt}, &out)
	out.Name = untrusted(out.Name, 100)
	return out, err
}

func revokeSigningKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in keyIDIn) (signingKeyOut, error) {
	if in.ID == "" {
		return signingKeyOut{}, errors.New("say which key: its id")
	}
	var out signingKeyOut
	err := c.apiJSON(ctx, "revoke_signing_key", http.MethodPost, apiPath("/api/v1/user/signing-keys/%s/revoke", in.ID), nil, &out)
	out.Name = untrusted(out.Name, 100)
	return out, err
}

func deleteSigningKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in keyIDIn) (deletedKeyOut, error) {
	if in.ID == "" {
		return deletedKeyOut{}, errors.New("say which key: its id")
	}
	if err := c.apiJSON(ctx, "delete_signing_key", http.MethodDelete, apiPath("/api/v1/user/signing-keys/%s", in.ID), nil, nil); err != nil {
		return deletedKeyOut{}, err
	}
	return deletedKeyOut{Deleted: in.ID}, nil
}

type verifySigIn struct {
	Repository string   `json:"repository" jsonschema:"the repository, owner/name"`
	Objects    []string `json:"objects" jsonschema:"full object names of commits or annotated tags, up to 200"`
}

type signatureOut struct {
	Object            string `json:"object"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	Explanation       string `json:"explanation"`
	Signer            string `json:"signer,omitempty"`
	KeyFingerprint    string `json:"key_fingerprint,omitempty"`
	SignerIsAuthor    bool   `json:"signer_is_author,omitempty"`
	SignerIsCommitter bool   `json:"signer_is_committer,omitempty"`
}

type signaturesOut struct {
	Summary string         `json:"summary"`
	Items   []signatureOut `json:"items"`
}

func verifySignatures(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in verifySigIn) (signaturesOut, error) {
	owner, name, ok := strings.Cut(in.Repository, "/")
	if !ok || owner == "" || name == "" || len(in.Objects) == 0 {
		return signaturesOut{}, errors.New("give the repository as owner/name and at least one object name")
	}
	var page struct {
		Items []signatureOut `json:"items"`
	}
	path := apiPath("/api/v1/repositories/%s/%s/signatures", owner, name) + "?shas=" + url.QueryEscape(strings.Join(in.Objects, ","))
	if err := c.apiJSON(ctx, "verify_signatures", http.MethodGet, path, nil, &page); err != nil {
		return signaturesOut{}, err
	}
	n := 0
	for i := range page.Items {
		page.Items[i].Signer = untrusted(page.Items[i].Signer, 100)
		if page.Items[i].State == "verified" {
			n++
		}
	}
	return signaturesOut{Summary: pluralN(n, "verified object") + " of " + pluralN(len(page.Items), "object"), Items: page.Items}, nil
}

func getInstallationKey(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (installationKeyOut, error) {
	var out installationKeyOut
	err := c.apiJSON(ctx, "get_signing_installation_key", http.MethodGet, "/api/v1/signing/installation-key", nil, &out)
	return out, err
}
