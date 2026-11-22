package api

// Run inputs in the API: the values a person gives when starting a run,
// and the inputs a pipeline declares at a ref, for the start form.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// inputValues turns the values a caller sent as JSON into the text forms
// pipeline.Input.Canon reads: a string as it is, a number as written, true
// or false, and a list of those as a JSON list of strings. A null is no
// value. Objects are refused, under the input's name.
func inputValues(raw map[string]json.RawMessage) (map[string]string, pipeline.InputErrors) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > pipeline.MaxInputs {
		return nil, pipeline.InputErrors{"": fmt.Sprintf("at most %d inputs", pipeline.MaxInputs)}
	}
	out, errs := map[string]string{}, pipeline.InputErrors{}
	for name, v := range raw {
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		var x any
		if err := dec.Decode(&x); err != nil {
			errs[name] = "not a JSON value"
			continue
		}
		switch t := x.(type) {
		case nil:
		case []any:
			items := make([]string, 0, len(t))
			for _, it := range t {
				s, ok := jsonScalar(it)
				if !ok {
					errs[name] = "a list holds single values"
					break
				}
				items = append(items, s)
			}
			if errs[name] == "" {
				b, _ := json.Marshal(items)
				out[name] = string(b)
			}
		default:
			s, ok := jsonScalar(t)
			if !ok {
				errs[name] = "give a single value or a list"
				continue
			}
			out[name] = s
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

func jsonScalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// writeInputErrors answers a request whose input values have problems:
// 422, with what is wrong with each value by input name, so a form can
// show it next to the field.
func writeInputErrors(w http.ResponseWriter, ie pipeline.InputErrors) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "the Sortie's inputs have problems: " + inputErrorSummary(ie), "fields": ie})
}

func inputErrorSummary(ie pipeline.InputErrors) string {
	names := make([]string, 0, len(ie))
	for n := range ie {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + ": " + ie[n]
	}
	return strings.Join(parts, "; ")
}

// inputsDetail describes input values for the audit trail.
func inputsDetail(vals map[string]string) string {
	if len(vals) == 0 {
		return ""
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + clip(vals[n], 80)
	}
	return ", inputs " + strings.Join(parts, " ")
}

// projectInputs returns the inputs the pipeline at ?ref= declares (the
// default branch if none), for the form that starts a run.
func (s *Server) projectInputs(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeErr(w, http.StatusNotFound, "no {projects} are configured")
		return
	}
	info, err := s.Projects.InputsAt(r.Context(), r.PathValue("name"), r.URL.Query().Get("ref"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}
