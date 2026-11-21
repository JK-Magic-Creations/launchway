package api

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// docs/public/api/openapi.yaml describes the HTTP API in OpenAPI 3.1: every route, who may call it, what it
// records in the audit trail, its path parameters, its headers and the one shape every error answer has. The file
// is made from the route table and the HTTP API reference's rows (so a route cannot be missing or invented), and
// these tests check that it is current and that it is a valid description. What it does not yet carry is each
// body's schema: that is a checklist item in docs/development/interface-quality.md.
//
//	LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/api -run TestOpenAPI   rewrites the file
const openAPIFile = "../../docs/public/api/openapi.yaml"

var pathParamRe = regexp.MustCompile(`\{([a-z_]+)\}`)

func openAPIPath(pattern string) (method, path string) {
	m, p, _ := strings.Cut(pattern, " ")
	return strings.ToLower(m), p
}

func opID(method, path string) string {
	p := strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(path)
	return method + strings.TrimRight(p, "_")
}

func tagOf(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) >= 3 && parts[0] == "api" && parts[1] == "runner":
		return "runner"
	case len(parts) >= 3 && parts[0] == "api":
		return parts[2]
	case len(parts) > 0:
		return parts[0]
	}
	return "other"
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "`", ""))
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

func buildOpenAPI(t *testing.T) []byte {
	t.Helper()
	docs := docRoutes(t)
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/responses/" + name} }
	paths := map[string]map[string]any{}
	for _, rt := range (&Server{}).routes() {
		method, path := openAPIPath(rt.pattern)
		name := docAccessNames[rt.access]
		d := docs[rt.pattern]
		var params []any
		for _, m := range pathParamRe.FindAllStringSubmatch(path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		// A change made from a browser session carries the header that says it came from Launchway's own pages.
		if method != "get" && rt.access != public && rt.access != runnerOnly {
			params = append(params, map[string]any{"$ref": "#/components/parameters/Csrf"})
		}
		if strings.Contains(path, "/webhooks/") {
			params = append(params, map[string]any{"$ref": "#/components/parameters/IdempotencyKey"})
		}
		responses := map[string]any{
			"200": map[string]any{"description": "Success. The body is described in the HTTP API reference (docs/reference/http-api.md)."},
			"400": ref("Error"), "429": ref("Error"), "500": ref("Error"), "503": ref("Error"),
		}
		if rt.access != public {
			responses["401"], responses["403"] = ref("Error"), ref("Error")
		}
		if len(params) > 0 && strings.Contains(path, "{") {
			responses["404"] = ref("Error")
		}
		if strings.TrimSpace(d.text) == "" {
			d.text = "Handled by " + strings.ToUpper(method) + " " + path + "; the HTTP API reference does not describe this route in a row of its own."
		}
		summary := firstSentence(d.text)
		if len(strings.Fields(summary)) < 3 {
			summary = strings.ToUpper(method) + " " + path + ": " + summary
		}
		op := map[string]any{
			"operationId": opID(method, path), "summary": summary, "description": strings.ReplaceAll(d.text, "`", ""),
			"tags": []string{tagOf(path)}, "responses": responses, "x-launchway-access": name,
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if rt.audit != "" {
			op["x-launchway-audit"] = rt.audit
		}
		switch rt.access {
		case public:
			op["security"] = []any{}
		case runnerOnly:
			op["security"] = []any{map[string]any{"runnerToken": []string{}}}
		}
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path][method] = op
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title": "Launchway HTTP API", "version": "v1",
			"description": "The API the web interface, the command line and the MCP server use. Every route is listed, with who may call it and what it records in the audit trail. Errors have one shape (error, code, next). Times are Unix milliseconds. Lists that can be long take limit and a cursor or before value and say where the next page starts. A breaking change gets a new version prefix (/api/v2); within v1 fields are added, never removed or retyped. Rate limits: failed authentication, sign-in checks and MCP calls are limited per caller and answer 429 with Retry-After; no other rate-limit headers are sent. Idempotency: a webhook call with an Idempotency-Key answers with the run the first call started.",
			"license":     map[string]any{"name": "AGPL-3.0-only", "identifier": "AGPL-3.0-only"},
		},
		"servers":  []any{map[string]any{"url": "/"}},
		"security": []any{map[string]any{"sessionCookie": []string{}}, map[string]any{"bearerToken": []string{}}},
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"sessionCookie": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie, "description": "The session a person gets by signing in to the web interface."},
				"bearerToken":   map[string]any{"type": "http", "scheme": "bearer", "description": "An API token made under People and access, sent as Authorization: Bearer."},
				"runnerToken":   map[string]any{"type": "http", "scheme": "bearer", "description": "A runner's token or identity, for /api/runner only."},
			},
			"parameters": map[string]any{
				"Csrf": map[string]any{"name": "X-Launchway-CSRF", "in": "header", "required": false, "schema": map[string]any{"type": "string", "enum": []string{"1"}},
					"description": "Required on a change made with a session cookie (not with a bearer token): it says the request came from Launchway's own pages, which a page of another site cannot send."},
				"IdempotencyKey": map[string]any{"name": "Idempotency-Key", "in": "header", "required": false, "schema": map[string]any{"type": "string", "maxLength": 200},
					"description": "A repeated call with the same key answers with the run the first call started."},
			},
			"responses": map[string]any{
				"Error": map[string]any{
					"description": "Something went wrong. The body says what, in plain words, with a code a program can branch on and what to do next.",
					"headers":     map[string]any{"Retry-After": map[string]any{"description": "Seconds to wait before trying again (429 and 503).", "schema": map[string]any{"type": "integer"}}},
					"content":     map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}},
				},
			},
			"schemas": map[string]any{
				"Error": map[string]any{
					"type": "object", "required": []string{"error", "code", "next"},
					"properties": map[string]any{
						"error":    map[string]any{"type": "string", "description": "What went wrong, in a plain sentence."},
						"code":     map[string]any{"type": "string", "description": "A stable lower-case word for programs: bad_request, unauthorized, forbidden, not_found, conflict, invalid, rate_limited, internal, unavailable and others a route may add."},
						"next":     map[string]any{"type": "string", "description": "What to do about it."},
						"problems": map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "For a pipeline or configuration that is not valid: each problem with its file, line, path and message."},
						"fields":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "For input values that do not fit: the message for each field by name."},
					},
				},
			},
		},
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte("# Generated from the route table and docs/reference/http-api.md by internal/api/openapi_test.go; do not edit.\n# LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/api -run TestOpenAPI rewrites it.\n"), out...)
}

func TestOpenAPIIsCurrent(t *testing.T) {
	want := buildOpenAPI(t)
	if os.Getenv("LAUNCHWAY_UPDATE_DOCS") == "1" {
		if err := os.MkdirAll("../../docs/public/api", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(openAPIFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(openAPIFile)
	if err != nil || string(got) != string(want) {
		t.Errorf("docs/public/api/openapi.yaml is not what the routes say: run LAUNCHWAY_UPDATE_DOCS=1 go test ./internal/api -run TestOpenAPI")
	}
}

// The committed file, read back, is a valid description and says what the route table says: the same operations,
// the same path parameters, resolvable references, unique operation ids, a success and the error shape in every
// operation, and the access class of every route.
func TestOpenAPIIsValidAndMatchesTheRoutes(t *testing.T) {
	raw, err := os.ReadFile(openAPIFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string `yaml:"openapi"`
		Info    struct{ Title, Version string }
		Paths   map[string]map[string]struct {
			OperationID string   `yaml:"operationId"`
			Summary     string   `yaml:"summary"`
			Tags        []string `yaml:"tags"`
			Access      string   `yaml:"x-launchway-access"`
			Audit       string   `yaml:"x-launchway-audit"`
			Parameters  []map[string]any
			Responses   map[string]map[string]any
		}
		Components struct {
			Responses  map[string]any
			Parameters map[string]any
			Schemas    map[string]any
		}
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the file is not YAML: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") || doc.Info.Title == "" || doc.Info.Version == "" {
		t.Errorf("openapi %q, info %+v", doc.OpenAPI, doc.Info)
	}
	routes := map[string]access{}
	audits := map[string]string{}
	for _, rt := range (&Server{}).routes() {
		routes[rt.pattern] = rt.access
		audits[rt.pattern] = rt.audit
	}
	ids := map[string]string{}
	have := map[string]bool{}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			key := strings.ToUpper(method) + " " + path
			have[key] = true
			acc, ok := routes[key]
			if !ok {
				t.Errorf("%s is described but is not a route", key)
				continue
			}
			if op.Access != docAccessNames[acc] || op.Audit != audits[key] {
				t.Errorf("%s: access %q audit %q, the route says %q %q", key, op.Access, op.Audit, docAccessNames[acc], audits[key])
			}
			if prev, dup := ids[op.OperationID]; dup || op.OperationID == "" {
				t.Errorf("%s: operationId %q is empty or also used by %s", key, op.OperationID, prev)
			}
			ids[op.OperationID] = key
			if len(strings.Fields(op.Summary)) < 2 || len(op.Tags) == 0 {
				t.Errorf("%s: needs a summary and a tag", key)
			}
			declared := map[string]bool{}
			for _, p := range op.Parameters {
				if name, _ := p["name"].(string); name != "" && p["in"] == "path" {
					declared[name] = true
				}
				if r, _ := p["$ref"].(string); r != "" {
					if _, ok := doc.Components.Parameters[strings.TrimPrefix(r, "#/components/parameters/")]; !ok {
						t.Errorf("%s: %s does not resolve", key, r)
					}
				}
			}
			for _, m := range pathParamRe.FindAllStringSubmatch(path, -1) {
				if !declared[m[1]] {
					t.Errorf("%s: the path parameter %s is not declared", key, m[1])
				}
			}
			ok2xx, hasErr := false, false
			for code, r := range op.Responses {
				if strings.HasPrefix(code, "2") {
					ok2xx = true
				}
				if ref, _ := r["$ref"].(string); ref != "" {
					hasErr = true
					if _, ok := doc.Components.Responses[strings.TrimPrefix(ref, "#/components/responses/")]; !ok {
						t.Errorf("%s: %s does not resolve", key, ref)
					}
				}
			}
			if !ok2xx || !hasErr {
				t.Errorf("%s: needs a success response and the error shape", key)
			}
		}
	}
	var missing []string
	for k := range routes {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("routes missing from the description: %s", strings.Join(missing, ", "))
	}
	if _, ok := doc.Components.Schemas["Error"]; !ok {
		t.Error("no Error schema")
	}
	// Every error code the server can give is a word the schema names.
	for status, c := range errorCodes {
		if !regexp.MustCompile(`^[a-z_]+$`).MatchString(c[0]) {
			t.Errorf("%d: code %q", status, c[0])
		}
	}
}
