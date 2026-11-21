package settings

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestVocabularyIsWellFormed(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range VocabGroups {
		groups[g.ID] = true
	}
	seen := map[string]bool{}
	key := regexp.MustCompile(`^[a-z][a-z_]*$`)
	for _, w := range vocabulary {
		if !key.MatchString(w.Key) || seen[w.Key] {
			t.Errorf("key %q is not a unique neutral key", w.Key)
		}
		seen[w.Key] = true
		if !groups[w.Group] {
			t.Errorf("%s: unknown group %q", w.Key, w.Group)
		}
		if strings.TrimSpace(w.Meaning) == "" || len(w.Meaning) > 160 {
			t.Errorf("%s: needs a plain meaning of one line (got %q)", w.Key, w.Meaning)
		}
		if w.Aviation.One == "" || w.Plain.One == "" {
			t.Errorf("%s: both sets need a word", w.Key)
		}
		for _, f := range []Forms{w.Aviation, w.Plain} {
			if strings.Contains(f.One+f.Many, "isation") || strings.ContainsAny(f.One+f.Many+f.Do, "<>&") {
				t.Errorf("%s: %+v", w.Key, f)
			}
			if f.One[0] < 'A' || f.One[0] > 'Z' {
				t.Errorf("%s: %q must be capitalized as a name", w.Key, f.One)
			}
			if (f.Do == "") != (f.Did == "") || (f.Do == "") != (f.Doing == "") {
				t.Errorf("%s: give all three verb forms or none: %+v", w.Key, f)
			}
		}
		if w.Group == "statuses" && w.Renameable {
			t.Errorf("%s: a status keeps its plain word, so it is not renameable", w.Key)
		}
	}
	// The words the ruling names exist.
	for _, k := range []string{"runner", "run", "queue", "run_log", "debrief", "schedule", "deploy", "environment", "rollback", "emergency_rollback", "canary",
		"post_deploy_watch", "toggle", "kill_switch", "release_unit", "home", "monitoring", "notifications", "registry", "artifact", "standards", "scan",
		"dependency_updates", "chaos_test", "learn", "first_run", "owner", "approver", "developer", "viewer", "queued", "running", "succeeded", "failed", "canceled", "rolled_back", "waiting"} {
		if !seen[k] {
			t.Errorf("the ruling's word %q is missing", k)
		}
	}
}

// No combat word and no real program, unit or rank name is a product word (ruling of 21 November).
func TestVocabularyHasNoCombatWords(t *testing.T) {
	bad := regexp.MustCompile(`(?i)\b(strike|target|kill|weapon|bomb|missile|attack|fire|shoot|assault|combat|raid|war)\b`)
	for _, w := range vocabulary {
		for _, f := range []Forms{w.Aviation, w.Plain} {
			if w.Key == "kill_switch" && f == w.Plain { // the plain, industry name of the thing
				continue
			}
			if bad.MatchString(f.One + " " + f.Many) {
				t.Errorf("%s: %q is a combat word", w.Key, f.One)
			}
		}
	}
}

func TestWordsFormsAndSentences(t *testing.T) {
	a, b := DefaultWords(), Terms{Set: "business"}.Words()
	if a.One("run") != "Sortie" || a.Many("run") != "Sorties" || b.One("run") != "Run" || b.Many("run") != "Runs" {
		t.Fatal("run")
	}
	if a.Verb("deploy", "do") != "launch" || a.Verb("deploy", "did") != "launched" || a.Verb("deploy", "doing") != "launching" {
		t.Fatal("deploy verbs")
	}
	if a.Verb("rollback", "did") != "returned to base" || b.Verb("rollback", "do") != "roll back" {
		t.Fatal("rollback verbs")
	}
	got := a.Say("A {run} on a {runner} may {deploy:do} to an {environment}; {runs} are {queued}.")
	if got != "A Sortie on a Station may launch to an Airfield; Sorties are Holding." {
		t.Fatalf("aviation: %q", got)
	}
	got = b.Say("A {run} on a {runner} may {deploy:do} to an {environment}; {Runs} wait in the {queue}; {unit}.")
	if got != "A run on a runner may deploy to an environment; Runs wait in the queue; Team." {
		t.Fatalf("business: %q", got)
	}
	if a.Say("{run:plain} and {nothing_known} and {") != "run and {nothing_known} and {" {
		t.Fatalf("plain/unknown: %q", a.Say("{run:plain} and {nothing_known} and {"))
	}
	o := Terms{Overrides: map[string]string{"run": "Mission"}}.Words()
	if o.One("run") != "Mission" || o.Many("run") != "Missions" || o.Verb("run", "do") != "mission" {
		t.Fatal("override")
	}
}

// docs/vocabulary.json and web/src/vocabulary.json are generated from the table; this keeps them equal.
// UPDATE_VOCABULARY=1 rewrites them.
func TestVocabularyMirrors(t *testing.T) {
	write := os.Getenv("UPDATE_VOCABULARY") != ""
	docsPath, webPath := "../../docs/vocabulary.json", "../../web/src/vocabulary.json"
	if _, err := os.Stat(docsPath); err != nil {
		t.Skip("docs not present")
	}
	webJSON, _ := json.MarshalIndent(map[string]any{"groups": VocabGroups, "words": WebVocabulary()}, "", "  ")
	webJSON = append(webJSON, '\n')
	check := func(path string, want []byte) {
		got, _ := os.ReadFile(path)
		if bytes.Equal(got, want) {
			return
		}
		if write {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Errorf("%s is out of date with internal/settings/vocabulary.go: run UPDATE_VOCABULARY=1 go test ./internal/settings -run TestVocabularyMirrors", path)
	}
	check(webPath, webJSON)

	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatal(err)
	}
	words, _ := json.MarshalIndent(map[string]any{"groups": VocabGroups, "words": Vocabulary()}, "  ", "  ")
	_ = words
	// Replace only the "words" key of the file, keeping every other key and its order.
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	type kv struct {
		k string
		v json.RawMessage
	}
	var all []kv
	for dec.More() {
		kt, _ := dec.Token()
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		all = append(all, kv{kt.(string), v})
	}
	vocab, _ := json.Marshal(map[string]any{"groups": VocabGroups, "words": Vocabulary()})
	found := false
	for i := range all {
		if all[i].k == "words" {
			all[i].v, found = vocab, true
		}
	}
	if !found {
		all = append(all, kv{"words", vocab})
	}
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, e := range all {
		var ind bytes.Buffer
		_ = json.Indent(&ind, e.v, "  ", "  ")
		k, _ := json.Marshal(e.k)
		out.WriteString("  " + string(k) + ": " + ind.String())
		if i < len(all)-1 {
			out.WriteString(",")
		}
		out.WriteString("\n")
	}
	out.WriteString("}\n")
	check(docsPath, out.Bytes())
}
