package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Test results: a run's, one by one, and a project's over time. Anyone who
// may see the run, or the project, may read them.

// runTests is a run's test results, step by step, with what stands out.
func (s *Server) runTests(w http.ResponseWriter, r *http.Request) {
	out, err := s.Engine.RunTests(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// runTestCases lists a run's results, failures first then the slowest:
// ?status=failed|passed|skipped|flaky, ?q= (in the name, suite, file or
// message), ?step= (with ?attempt= for an earlier attempt), ?limit=,
// ?offset=.
func (s *Server) runTestCases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cq := engine.CaseQuery{Status: q.Get("status"), Search: q.Get("q"), Step: q.Get("step")}
	for name, dst := range map[string]*int{"attempt": &cq.Attempt, "limit": &cq.Limit, "offset": &cq.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, name+" must be a whole number")
				return
			}
			*dst = n
		}
	}
	if len(cq.Search) > 200 {
		writeErr(w, http.StatusBadRequest, "the search text is longer than 200 bytes")
		return
	}
	page, err := s.Engine.RunTestCases(r.PathValue("id"), cq)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// runTestCase is one of a run's results in full: trace and captured output.
func (s *Server) runTestCase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("case"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, fmt.Errorf("%w: test result %s", engine.ErrNotFound, r.PathValue("case")))
		return
	}
	c, err := s.Engine.TestCase(r.PathValue("id"), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// projectRef is the ref a project's test pages look at: ?ref= (a branch
// name or a full ref), else the project's default branch, else the latest
// ref with results ("").
func (s *Server) projectRef(r *http.Request) string {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	switch {
	case ref == "all":
		return ""
	case ref != "":
		if !strings.HasPrefix(ref, "refs/") {
			ref = "refs/heads/" + ref
		}
		return ref
	case s.Projects != nil:
		if p, err := s.Projects.Get(r.PathValue("name")); err == nil && p.DefaultBranch != "" {
			return "refs/heads/" + p.DefaultBranch
		}
	}
	return ""
}

func intParam(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be a whole number", engine.ErrInvalid, name)
	}
	return n, nil
}

// projectTests is a project's tests over the last runs of a ref (?ref=,
// ?runs= how many, default 30): totals, slowest, most failing and flaky.
func (s *Server) projectTests(w http.ResponseWriter, r *http.Request) {
	n, err := intParam(r, "runs")
	if err != nil {
		s.fail(w, err)
		return
	}
	out, err := s.Engine.TestOverview(r.PathValue("name"), s.projectRef(r), n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// testHistory is one test's results in a project's last runs: ?key= (from
// a result), ?ref= (or ref=all), ?limit=.
func (s *Server) testHistory(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" || len(key) > 64 {
		writeErr(w, http.StatusBadRequest, "say which test: ?key= as given with its results")
		return
	}
	n, err := intParam(r, "limit")
	if err != nil {
		s.fail(w, err)
		return
	}
	ref := s.projectRef(r)
	if r.URL.Query().Get("ref") == "" {
		ref = "" // every ref, unless one is asked for
	}
	out, err := s.Engine.TestHistory(r.PathValue("name"), key, ref, n)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
