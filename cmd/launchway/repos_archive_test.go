package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The exporter script reads a source server's REST API (here a fake that
// requires the token), writes the document, and import-archive stores it; the
// list finds a word in a comment and show prints the thread.
func TestExportScriptAndImportArchiveRoundTrip(t *testing.T) {
	for _, tool := range []string{"sh", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	const token = "src-token-6b1f09aa"
	var sawToken bool
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		sawToken = true
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/repos/acme/shop/issues" && r.URL.Query().Get("type") == "issues":
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"number":2,"title":"Crash on checkout","state":"closed","body":"It crashes.\n\nSteps: 1, 2","user":{"login":"bob"},
				"labels":[{"name":"bug"}],"milestone":{"title":"v1"},"assignees":[{"login":"ada"}],"html_url":"https://old.example/acme/shop/issues/2",
				"created_at":"2024-01-02T03:04:05Z","updated_at":"2024-01-03T03:04:05Z","closed_at":"2024-01-03T03:04:05Z"},
				{"number":1,"title":"Add a receipt page","state":"open","body":"Please.","user":{"login":"ada"},"labels":[],"assignees":[],"created_at":"2024-01-01T00:00:00Z"}]`))
		case r.URL.Path == "/api/v1/repos/acme/shop/issues/2/comments":
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":77,"user":{"login":"ada"},"body":"Fixed by the zebra patch","created_at":"2024-01-03T01:00:00Z"}]`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer src.Close()

	dir := t.TempDir()
	cmd := exec.Command("sh", "../../scripts/export-issues.sh", src.URL, "acme/shop")
	cmd.Env = append(os.Environ(), "SOURCE_TOKEN="+token)
	doc, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("export: %v: %s", err, ee.Stderr)
		}
		t.Fatalf("export: %v", err)
	}
	if !sawToken || strings.Contains(string(doc), token) {
		t.Fatalf("token handling: saw %v, in the document %v", sawToken, strings.Contains(string(doc), token))
	}
	var parsed struct {
		Format string `json:"format"`
		Issues []struct {
			Number   int `json:"number"`
			Comments []struct {
				ID string `json:"id"`
			} `json:"comments"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil || parsed.Format != "launchway-archive/1" || len(parsed.Issues) != 2 || parsed.Issues[0].Number != 1 || parsed.Issues[1].Comments[0].ID != "77" {
		t.Fatalf("document: %v %s", err, doc)
	}
	file := filepath.Join(dir, "issues.json")
	if err := os.WriteFile(file, doc, 0o600); err != nil {
		t.Fatal(err)
	}

	hc := newHostedCLI(t)
	var out bytes.Buffer
	ada := hc.client("ada", "root", &out)
	if err := ada.reposCreate([]string{"shop", "--owner", "acme"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ada.reposImportArchive([]string{"acme/shop", file}); err != nil || !strings.Contains(out.String(), "Stored 2 issues and 1 comments") {
		t.Fatalf("import: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposImportArchive([]string{"acme/shop", file}); err != nil || !strings.Contains(out.String(), "2 replaced") {
		t.Fatalf("again: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposArchive([]string{"list", "acme/shop", "--query", "ZEBRA"}); err != nil || !strings.Contains(out.String(), "#2") || strings.Contains(out.String(), "#1 ") {
		t.Fatalf("search: %v %q", err, out.String())
	}
	out.Reset()
	if err := ada.reposArchive([]string{"show", "acme/shop", "2"}); err != nil || !strings.Contains(out.String(), "Crash on checkout") || !strings.Contains(out.String(), "zebra patch") {
		t.Fatalf("show: %v %q", err, out.String())
	}
	if err := hc.client("otto", "ops", &bytes.Buffer{}).reposImportArchive([]string{"acme/shop", file}); err == nil {
		t.Fatal("an operator imported into an archive")
	}
}
