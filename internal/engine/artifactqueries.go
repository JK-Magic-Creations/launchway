package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Reading artifacts back, for the API.

// ArtifactView is an artifact as the API shows it.
type ArtifactView struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	RunNumber int    `json:"run_number"`
	Step      string `json:"step"`
	Attempt   int    `json:"attempt"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	// Status is uploading, kept, failed, expired or deleted.
	Status    string `json:"status"`
	FileCount int    `json:"file_count"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	KeptAt    int64  `json:"kept_at,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	EndedBy   string `json:"ended_by,omitempty"`
	Note      string `json:"note,omitempty"`
	// Reused says the artifact belongs to a step this run reused from the
	// run RunID, an earlier one.
	Reused bool `json:"reused,omitempty"`
	// Files are its files, at most maxListedFiles of them.
	Files          []ArtifactFileInfo `json:"files"`
	FilesTruncated bool               `json:"files_truncated,omitempty"`
}

// maxListedFiles bounds the files listed with each artifact.
const maxListedFiles = 1000

func (a *artifactRow) view(files []ArtifactFileInfo) *ArtifactView {
	v := &ArtifactView{ID: a.ID, RunID: a.RunID, RunNumber: a.RunNumber, Step: a.StepID, Attempt: a.Attempt, Name: a.Name,
		Kind: a.Kind, Status: a.Status, FileCount: a.Files, Bytes: a.Bytes, CreatedAt: a.CreatedAt, KeptAt: a.KeptAt,
		ExpiresAt: a.ExpiresAt, EndedAt: a.EndedAt, EndedBy: a.EndedBy, Note: a.Note, Files: files}
	if v.Files == nil {
		v.Files = []ArtifactFileInfo{}
	}
	if a.Status != artKept {
		v.ExpiresAt = 0
	}
	return v
}

// RunArtifacts lists a run's artifacts, step by step in the pipeline's
// order and every attempt's, and those of the steps it reused from earlier
// runs.
type RunArtifacts struct {
	Artifacts []*ArtifactView `json:"artifacts"`
	// Declared are the steps that declare artifacts.
	Declared []string `json:"declared"`
	// Available says whether this server keeps artifacts; Problem says why
	// not.
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
	// MaxRetainMs is the longest the server keeps any artifact.
	MaxRetainMs int64 `json:"max_retain_ms"`
}

// RunArtifacts lists a run's artifacts.
func (e *Engine) RunArtifacts(runID string) (*RunArtifacts, error) {
	st, err := e.runState(runID)
	if err != nil {
		return nil, err
	}
	out := &RunArtifacts{Artifacts: []*ArtifactView{}, Declared: []string{}, Available: e.arts.Store != nil,
		Problem: e.arts.StoreProblem, MaxRetainMs: e.live().arts.MaxRetain.Milliseconds()}
	if out.Available {
		out.Problem = ""
	}
	for _, s := range st.Pipeline.Steps {
		if len(s.Artifacts) > 0 || (s.Reports != nil && len(s.Reports.SBOM) > 0) {
			out.Declared = append(out.Declared, s.ID)
		}
	}
	if !e.knows(schemaArtifacts) {
		return out, nil
	}
	position := map[string]int{}
	for i, id := range st.Order {
		position[id] = i
	}
	var rows []*artifactRow
	add := func(q string, args ...any) error {
		r, err := e.db.Query(`SELECT `+artifactCols+` FROM artifacts WHERE `+q, args...)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			a, err := scanArtifact(r)
			if err != nil {
				return err
			}
			rows = append(rows, a)
		}
		return r.Err()
	}
	if err := add(`run_id = ?`, runID); err != nil {
		return nil, err
	}
	reused := map[string]bool{}
	for _, id := range st.Order {
		s := st.Steps[id]
		if s.ReusedFrom == "" {
			continue
		}
		if err := add(`run_id = ? AND step_id = ? AND attempt = ?`, s.ReusedFrom, id, s.ReusedAttempt); err != nil {
			return nil, err
		}
		reused[s.ReusedFrom+"\x00"+id] = true
	}
	slices.SortStableFunc(rows, func(a, b *artifactRow) int {
		if d := position[a.StepID] - position[b.StepID]; d != 0 {
			return d
		}
		if a.Attempt != b.Attempt {
			return a.Attempt - b.Attempt
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, a := range rows {
		files, more, err := e.listedFiles(a.ID)
		if err != nil {
			return nil, err
		}
		v := a.view(files)
		v.FilesTruncated = more
		v.Reused = a.RunID != runID && reused[a.RunID+"\x00"+a.StepID]
		out.Artifacts = append(out.Artifacts, v)
	}
	return out, nil
}

func (e *Engine) listedFiles(id string) ([]ArtifactFileInfo, bool, error) {
	rows, err := e.db.Query(`SELECT path, size, sha256, mode FROM artifact_files WHERE artifact_id = ? ORDER BY path LIMIT ?`, id, maxListedFiles+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []ArtifactFileInfo
	for rows.Next() {
		var f ArtifactFileInfo
		if err := rows.Scan(&f.Path, &f.Size, &f.SHA256, &f.Mode); err != nil {
			return nil, false, err
		}
		out = append(out, f)
	}
	more := len(out) > maxListedFiles
	if more {
		out = out[:maxListedFiles]
	}
	return out, more, rows.Err()
}

// keptArtifact finds a kept artifact of a run.
func (e *Engine) keptArtifact(runID, artifactID string) (*artifactRow, error) {
	if !e.knows(schemaArtifacts) {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, artifactID)
	}
	a, err := artifactByID(e.db, artifactID)
	if err != nil {
		return nil, err
	}
	if a.RunID != runID {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, artifactID)
	}
	if a.Status != artKept {
		return nil, fmt.Errorf("%w: artifact %s is %s", ErrNotFound, a.Name, a.statusText())
	}
	return a, nil
}

// OpenArtifactFile opens a file of a kept artifact of a run, from offset.
func (e *Engine) OpenArtifactFile(ctx context.Context, runID, artifactID, filePath string, offset int64) (*ArtifactFile, error) {
	a, err := e.keptArtifact(runID, artifactID)
	if err != nil {
		return nil, err
	}
	return e.openArtifactFile(ctx, a, filePath, offset)
}

// ArtifactArchive is a kept artifact and every one of its files, for an
// archive of them all.
type ArtifactArchive struct {
	Name  string
	Step  string
	Files []ArtifactFileInfo
	open  func(ctx context.Context, path string) (*ArtifactFile, error)
}

// Open opens one of the archive's files.
func (a *ArtifactArchive) Open(ctx context.Context, path string) (*ArtifactFile, error) {
	return a.open(ctx, path)
}

// ArtifactForArchive finds a kept artifact of a run and lists its files.
func (e *Engine) ArtifactForArchive(runID, artifactID string) (*ArtifactArchive, error) {
	a, err := e.keptArtifact(runID, artifactID)
	if err != nil {
		return nil, err
	}
	files, err := artifactFiles(e.db, a.ID)
	if err != nil {
		return nil, err
	}
	out := &ArtifactArchive{Name: a.Name, Step: a.StepID}
	for _, f := range files {
		out.Files = append(out.Files, f.ArtifactFileInfo)
	}
	out.open = func(ctx context.Context, path string) (*ArtifactFile, error) {
		return e.openArtifactFile(ctx, a, path, 0)
	}
	return out, nil
}

// attachArtifactCounts adds the number of kept artifacts to runs as listed.
func (e *Engine) attachArtifactCounts(runs []*RunSummary) error {
	if !e.knows(schemaArtifacts) || len(runs) == 0 {
		return nil
	}
	byID := map[string]*RunSummary{}
	idList := make([]string, 0, len(runs))
	for _, r := range runs {
		byID[r.ID] = r
		idList = append(idList, r.ID)
	}
	return inChunks(idList, func(marks string, args []any) error {
		rows, err := e.db.Query(`SELECT run_id, COUNT(*) FROM artifacts WHERE status = 'kept' AND run_id IN (`+marks+`) GROUP BY run_id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			if r := byID[id]; r != nil {
				r.Artifacts = n
			}
		}
		return rows.Err()
	})
}
