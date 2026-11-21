package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/settings"
)

// Every error answer has the same three parts: error (what went wrong, in plain words), code (a stable word for
// programs) and next (what to do). See docs/development/interface-quality.md.
func TestErrorAnswersHaveOneShape(t *testing.T) {
	for status := range errorCodes {
		rec := httptest.NewRecorder()
		writeErr(rec, status, "something plain happened")
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if body["error"] != "something plain happened" {
			t.Errorf("%d: error = %q", status, body["error"])
		}
		if !strings.Contains("abcdefghijklmnopqrstuvwxyz_", body["code"][:1]) || body["code"] != strings.ToLower(body["code"]) || strings.ContainsAny(body["code"], " -") {
			t.Errorf("%d: code %q is not a lower-case word", status, body["code"])
		}
		if len(strings.Fields(body["next"])) < 4 || !strings.HasSuffix(body["next"], ".") {
			t.Errorf("%d: next %q is not a sentence", status, body["next"])
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%d: content type %q", status, rec.Header().Get("Content-Type"))
		}
	}
}

func TestErrorAnswersKeepWhatTheHandlerAdded(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusUnprocessableEntity, map[string]any{"error": "the pipeline has problems", "problems": []string{"a"}, "code": "pipeline_invalid", "next": "Fix line 3."})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "pipeline_invalid" || body["next"] != "Fix line 3." || body["problems"] == nil {
		t.Errorf("a handler's own parts were replaced: %v", body)
	}
	// An answer that is not an error is left alone, and so is a failing answer that is not an error body.
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]string{"error": "not an error"})
	if strings.Contains(rec.Body.String(), "code") {
		t.Errorf("a 200 answer was changed: %s", rec.Body.String())
	}
}

// A handler writes {runner}; the answer says the word of the installation's set, in `error` and `next` only.
func TestErrorSentencesUseTheInstallationsWords(t *testing.T) {
	defer settings.SetCurrentTerms(settings.Terms{})
	settings.SetCurrentTerms(settings.Terms{})
	got := withErrorShape(400, map[string]string{"error": "the {runner} has no ID", "code": "bad_request", "next": "Enrol the {runner}; {rollback:do} if you must."}).(map[string]string)
	if got["error"] != "the Station has no ID" || got["code"] != "bad_request" || got["next"] != "Enrol the Station; return to base if you must." {
		t.Fatalf("aviation: %+v", got)
	}
	settings.SetCurrentTerms(settings.Terms{Set: "business"})
	got = withErrorShape(400, map[string]string{"error": "the {runner} has no ID"}).(map[string]string)
	if got["error"] != "the runner has no ID" {
		t.Fatalf("business: %+v", got)
	}
}
