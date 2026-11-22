package api

// POST /api/v1/import/pipeline: take the pipeline files of another system and
// return the proposed Launchway pipeline with findings. Nothing is stored: the
// files live for the length of the request, and the reader is a pure function
// that bounds size, depth and expansion and never evaluates anything.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/importer"
)

// importBodyLimit bounds the request body; the importer applies its own, smaller
// limits to the files themselves.
const importBodyLimit = 6 << 20

// ImportRequest is the body of POST /api/v1/import/pipeline.
type ImportRequest struct {
	// Format is a format id (see the guide), or "auto" or empty to detect it.
	Format string `json:"format,omitempty" jsonschema:"hosted-workflows, stages-yaml, groovy-declarative, jobs-config, tasks-pipelines, or auto"`
	// Files are the source files with their paths in the repository.
	Files []ImportFile `json:"files" jsonschema:"the source files: path in the repository and text"`
	// Root imports only this file (its path) when several were sent.
	Root string `json:"root,omitempty" jsonschema:"import only this file, named by its path in the files sent"`
	// Dry marks every step that delivers as not yet enabled, behind the input enable_delivery.
	Dry          bool              `json:"dry,omitempty" jsonschema:"switch off steps that push, release or deploy until the input enable_delivery is true"`
	Name         string            `json:"name,omitempty" jsonschema:"the pipeline's name; default from the file"`
	SecretStore  string            `json:"secret_store,omitempty" jsonschema:"the Station secret store the written secret references name; default vault"`
	DefaultImage string            `json:"default_image,omitempty" jsonschema:"the image for jobs that name none"`
	NoTemplates  bool              `json:"no_templates,omitempty" jsonschema:"write explicit steps even where a built-in template fits"`
	Templates    map[string]string `json:"templates,omitempty" jsonschema:"fix-up: job id to template name, or none"`
	SecretKeys   map[string]string `json:"secret_keys,omitempty" jsonschema:"fix-up: source secret name to the key it is stored under (names only)"`
}

// ImportFile is one file of a request.
type ImportFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ImportResponse is the answer.
type ImportResponse struct {
	Plans    []*importer.Plan    `json:"plans"`
	Detected []importer.Detected `json:"detected"`
	Message  string              `json:"message,omitempty"`
}

func (s *Server) importPipeline(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, importBodyLimit))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "the request is too large; send fewer or smaller files")
		return
	}
	var req ImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "the body is not the JSON of an import request: "+err.Error())
		return
	}
	res, err := RunImport(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// RunImport runs a request; the CLI and the MCP tool call the same function.
func RunImport(req ImportRequest) (*ImportResponse, error) {
	files := make([]importer.File, len(req.Files))
	for i, f := range req.Files {
		files[i] = importer.File{Path: f.Path, Content: []byte(f.Content)}
	}
	res, err := importer.Import(importer.Request{
		Format: req.Format, Files: files, Root: req.Root,
		Options: importer.Options{Name: req.Name, Dry: req.Dry, SecretStore: req.SecretStore, DefaultImage: req.DefaultImage, NoTemplates: req.NoTemplates, Templates: req.Templates, SecretKeys: req.SecretKeys},
	})
	out := &ImportResponse{Plans: []*importer.Plan{}, Detected: []importer.Detected{}}
	if res != nil {
		if res.Plans != nil {
			out.Plans = res.Plans
		}
		if res.Detected != nil {
			out.Detected = res.Detected
		}
	}
	if errors.Is(err, importer.ErrNothing) {
		out.Message = "No pipeline files of a known format were found in what was sent. Name the format, or send the files with their paths (for example .github/workflows/ci.yml)."
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
