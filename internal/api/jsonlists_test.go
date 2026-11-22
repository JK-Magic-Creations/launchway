package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type listInner struct {
	Chain []string `json:"chain"`
	Tags  []string `json:"tags,omitempty"`
}

type listOuter struct {
	Items   []listInner       `json:"items"`
	Raw     json.RawMessage   `json:"raw"`
	Bytes   []byte            `json:"bytes"`
	Nested  *listInner        `json:"nested"`
	ByName  map[string][]int  `json:"by_name"`
	Pointer []*listInner      `json:"pointers"`
	Hidden  []string          `json:"-"`
	Any     any               `json:"any"`
	Fixed   [2]int            `json:"fixed"`
	Named   map[string]string `json:"named"`
}

// A list in an answer is [] when it is empty, never null (a null chain once blanked a whole page).
func TestAnswersNeverSendNullForAList(t *testing.T) {
	for name, v := range map[string]any{
		"a struct by value":   listOuter{Items: []listInner{{}}, Nested: &listInner{}, Pointer: []*listInner{{}}, Any: &listInner{}},
		"a pointer to struct": &listOuter{Items: []listInner{{}}, Nested: &listInner{}},
		"a list of structs":   []listInner{{}, {}},
	} {
		w := httptest.NewRecorder()
		writeJSON(w, 200, v)
		body := w.Body.String()
		for _, bad := range []string{`"chain":null`, `"items":null`, `"pointers":null`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: %s in %s", name, bad, body)
			}
		}
		if !strings.Contains(body, `"chain":[]`) {
			t.Errorf("%s: no empty chain in %s", name, body)
		}
		if strings.Contains(body, `"tags"`) {
			t.Errorf("%s: omitempty list written: %s", name, body)
		}
	}
	// What is not a list stays what it is, and the caller's own value is not changed by writing it.
	in := listOuter{}
	w := httptest.NewRecorder()
	writeJSON(w, 200, in)
	if in.Items != nil {
		t.Error("the caller's value was changed")
	}
	if b := w.Body.String(); !strings.Contains(b, `"raw":null`) || !strings.Contains(b, `"nested":null`) || !strings.Contains(b, `"items":[]`) {
		t.Errorf("%s", b)
	}
}
