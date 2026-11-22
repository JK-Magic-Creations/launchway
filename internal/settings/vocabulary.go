package settings

import (
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
)

// The vocabulary is the ONE table of Launchway's own words. Everything that
// shows a word to a person reads it: the web app (served in the configuration
// overview, mirrored in web/src/vocabulary.json), the command line, the
// sentences the API and the notifications build, the MCP descriptions and the
// documentation tools (docs/vocabulary.json, scripts/vocabulary). The mirrors
// are generated from this table and a test keeps them equal:
//
//	UPDATE_VOCABULARY=1 go test ./internal/settings -run TestVocabularyMirrors
//
// To add a word: add a Word below (key neutral and permanent, both sets, a plain
// meaning), run the command above, then `scripts/vocabulary-fix --docs` and
// `make vocabulary`. A key is never renamed or reused: it is how a word is found
// by code. Words are display labels only; flags, API fields, enum values, URLs and
// file names never change with a set or a rename.

// Forms are the shapes of one word in one set. One and Many are capitalized as a
// name ("Sortie", "Sorties"); the verb forms are lower case ("launch", "launched",
// "launching") and exist only for a word used as a verb.
type Forms struct {
	One   string `json:"one"`
	Many  string `json:"many,omitempty"`
	Do    string `json:"do,omitempty"`
	Did   string `json:"did,omitempty"`
	Doing string `json:"doing,omitempty"`
}

// Word is one concept: its stable key, its word in each built-in set and a plain
// meaning for tooltips and the vocabulary page.
type Word struct {
	Key        string   `json:"key"`
	Group      string   `json:"group"`
	Aviation   Forms    `json:"aviation"`
	Plain      Forms    `json:"plain"`
	Meaning    string   `json:"meaning"`
	Renameable bool     `json:"renameable"`
	Identifier string   `json:"identifier,omitempty"`
	MustNotBe  []string `json:"mustNotBeCalled,omitempty"`
}

// VocabGroups are the groups, in the order the Words page shows them, with a title.
var VocabGroups = []struct{ ID, Title string }{
	{"levels", "Levels"}, {"work", "Work"}, {"delivery", "Delivery"}, {"platform", "Platform"}, {"roles", "Roles"}, {"statuses", "Statuses"},
}

func f(one string) Forms { return Forms{One: one} }
func fv(one, many, do, did, doing string) Forms {
	return Forms{One: one, Many: many, Do: do, Did: did, Doing: doing}
}

// vocabulary is the table. Order is the order of the Words page.
var vocabulary = []Word{
	// levels
	{Key: "installation", Group: "levels", Aviation: fv("Base", "Bases", "", "", ""), Plain: fv("Installation", "Installations", "", "", ""),
		Meaning: "The whole installation, and every customer's own walled-off space on the hosted service: the top level.", Renameable: true,
		MustNotBe: []string{"tenant", "workspace", "account", "instance", "organization (for the top level)"}},
	{Key: "organization", Group: "levels", Aviation: fv("Wing", "Wings", "", "", ""), Plain: fv("Organization", "Organizations", "", "", ""),
		Meaning: "The group that owns the projects: the second level.", Renameable: true, Identifier: "organization (kind name today: organisation)",
		MustNotBe: []string{"org", "organisation", "company (in product text)", "tenant"}},
	{Key: "unit", Group: "levels", Aviation: fv("Squadron", "Squadrons", "", "", ""), Plain: fv("Team", "Teams", "", "", ""),
		Meaning: "An optional group of projects with shared settings: the third level.", Renameable: true,
		MustNotBe: []string{"group", "namespace", "workspace", "team (when the default words are in effect)"}},
	{Key: "project", Group: "levels", Aviation: fv("Project", "Projects", "", "", ""), Plain: fv("Project", "Projects", "", "", ""),
		Meaning: "One repository and its pipeline: the fourth level.", Renameable: true,
		MustNotBe: []string{"repo (as the level)", "app (that is an application, a deployed thing)"}},

	// work
	{Key: "runner", Group: "work", Aviation: f("Station"), Plain: f("Runner"),
		Meaning: "A machine or service that does the work of a pipeline's steps.", Renameable: true, MustNotBe: []string{"agent (for a Station)", "worker", "node"}},
	{Key: "runner_group", Group: "work", Aviation: f("Flight line"), Plain: f("Runner group"),
		Meaning: "A group of runners that share a purpose, such as every runner that can build images.", Renameable: true, MustNotBe: []string{"runner pool (in text a person reads)", "fleet"}},
	{Key: "run", Group: "work", Aviation: fv("Sortie", "Sorties", "", "", ""), Plain: f("Run"),
		Meaning: "One execution of a pipeline, from the first step to the last.", Renameable: true, MustNotBe: []string{"build (for any run)", "job (for a whole run)", "execution"}},
	{Key: "queue", Group: "work", Aviation: f("Holding pattern"), Plain: f("Queue"),
		Meaning: "Where runs wait until a runner or a slot is free, and the reason each one is waiting.", Renameable: true, MustNotBe: []string{"backlog"}},
	{Key: "run_log", Group: "work", Aviation: f("Flight recorder"), Plain: f("Run log"),
		Meaning: "The recorded output of every step of a run, kept so you can read what happened.", Renameable: true, MustNotBe: []string{"console output"}},
	{Key: "debrief", Group: "work", Aviation: f("Debrief"), Plain: f("Run summary"),
		Meaning: "The report of a run's tests: what passed, what failed and why, which are flaky, and its exports.", Renameable: true, MustNotBe: []string{"post-mortem (for every run)"}},
	{Key: "schedule", Group: "work", Aviation: f("Patrol"), Plain: f("Scheduled run"),
		Meaning: "A run that starts by itself at set times, such as every night.", Renameable: true, MustNotBe: []string{"cron job (in text a person reads)"}},

	// delivery
	{Key: "deploy", Group: "delivery", Aviation: fv("Launch", "Launches", "launch", "launched", "launching"), Plain: fv("Deploy", "Deploys", "deploy", "deployed", "deploying"),
		Meaning: "Put a version of your software into an environment, such as testing or production.", Renameable: true, MustNotBe: []string{"release (for the act of putting live)", "push to production"}},
	{Key: "environment", Group: "delivery", Aviation: f("Airfield"), Plain: f("Environment"),
		Meaning: "A place your software runs, such as development, testing or production.", Renameable: true, MustNotBe: []string{"stage (for a place)", "target"}},
	{Key: "rollback", Group: "delivery", Aviation: fv("Return to base", "Returns to base", "return to base", "returned to base", "returning to base"), Plain: fv("Rollback", "Rollbacks", "roll back", "rolled back", "rolling back"),
		Meaning: "Go back to the previous working version after a deploy.", Renameable: true, MustNotBe: []string{"revert (for a deployment)", "undo"}},
	{Key: "emergency_rollback", Group: "delivery", Aviation: fv("Abort", "Aborts", "abort", "aborted", "aborting"), Plain: fv("Emergency rollback", "Emergency rollbacks", "roll back immediately", "rolled back immediately", "rolling back immediately"),
		Meaning: "Go back at once, skipping the checks and waits, when production is hurt.", Renameable: true, MustNotBe: []string{"panic button", "kill (for a deployment)"}},
	{Key: "canary", Group: "delivery", Aviation: f("Pathfinder"), Plain: f("Canary"),
		Meaning: "A small first share of traffic or machines that gets the new version before everyone, to catch problems early.", Renameable: true, MustNotBe: []string{"test group"}},
	{Key: "post_deploy_watch", Group: "delivery", Aviation: f("Overwatch"), Plain: f("Post-deploy watch"),
		Meaning: "The time after a deploy when Launchway keeps watching health signals and can go back by itself.", Renameable: true, MustNotBe: []string{"soak (for the watch)"}},
	{Key: "toggle", Group: "delivery", Aviation: f("Switch"), Plain: f("Feature toggle"),
		Meaning: "A setting that turns a feature on or off without a new deploy.", Renameable: true, MustNotBe: []string{"feature flag (in text a person reads)"}},
	{Key: "kill_switch", Group: "delivery", Aviation: f("Master switch"), Plain: f("Kill switch"),
		Meaning: "A switch that turns a feature off for everyone at once.", Renameable: true, MustNotBe: []string{"kill (for the act)"}},
	{Key: "release_unit", Group: "delivery", Aviation: f("Formation"), Plain: f("Release group"),
		Meaning: "Several things that are released together and move as one.", Renameable: true, MustNotBe: []string{"bundle"}},
	{Key: "preflight", Group: "delivery", Aviation: f("Preflight"), Plain: f("Checks"),
		Meaning: "The checks made before a run, a merge or a deploy.", Renameable: true, MustNotBe: []string{"pre-checks", "gate checks"}},
	{Key: "checklist", Group: "delivery", Aviation: f("Checklist"), Plain: f("Checklist"),
		Meaning: "The list of things that must be true before a change goes on.", Renameable: true, MustNotBe: []string{"gate list"}},
	{Key: "clearance", Group: "delivery", Aviation: f("Clearance"), Plain: f("Approval"),
		Meaning: "A person decides whether a change may go on, by saying Go or No-go.", Renameable: true, MustNotBe: []string{"sign-off", "gate (for the thing a person gives)"}},
	{Key: "go", Group: "delivery", Aviation: f("Go"), Plain: f("Approve"),
		Meaning: "The recorded answer that lets the run continue. The button says Approve in both sets.", Renameable: true},
	{Key: "no_go", Group: "delivery", Aviation: f("No-go"), Plain: f("Reject"),
		Meaning: "The recorded answer that fails the step. The button says Reject in both sets.", Renameable: true, MustNotBe: []string{"nogo", "no go", "NoGo"}},
	{Key: "hold", Group: "delivery", Aviation: f("Hold"), Plain: f("Freeze"),
		Meaning: "A time when changes may not go out.", Renameable: true, MustNotBe: []string{"blackout", "code freeze (use freeze in the plain set)"}},
	{Key: "flight_plan", Group: "delivery", Aviation: f("Flight plan"), Plain: f("Pipeline template"),
		Meaning: "A complete, ready-made pipeline for a kind of project, with a few things to fill in.", Renameable: true,
		MustNotBe: []string{"blueprint", "boilerplate"}},
	{Key: "maneuver", Group: "delivery", Aviation: f("Maneuver"), Plain: f("Step template"),
		Meaning: "A reusable step, or a few steps, that a pipeline uses by name: build an image, deliver to an environment.", Renameable: true,
		MustNotBe: []string{"macro", "action (that is another product's word)", "module"}},
	{Key: "hangar", Group: "delivery", Aviation: f("Hangar"), Plain: f("Template library"),
		Meaning: "The library of Flight plans and Maneuvers that ships with Launchway, and the page that shows it.", Renameable: true,
		MustNotBe: []string{"marketplace", "store"}},
	{Key: "payload", Group: "delivery", Aviation: f("Payload"), Plain: f("Contents"),
		Meaning: "What a change delivers.", Renameable: true, MustNotBe: []string{"delivery contents", "manifest (that is a file)"}},
	{Key: "in_flight", Group: "delivery", Aviation: f("In flight"), Plain: f("In progress"),
		Meaning: "A change on its way to production.", Renameable: true},
	{Key: "launch_line", Group: "delivery", Aviation: f("Launch line"), Plain: f("Launch line"),
		Meaning: "The path a change travels, from commit to production. Named after the product, so the same in both sets.", Renameable: true,
		MustNotBe: []string{"pipeline view (a pipeline is the definition; the launch line is the path of one change)"}},

	// platform
	{Key: "home", Group: "platform", Aviation: f("Tower"), Plain: f("Home"),
		Meaning: "The first page: what needs you now, and a map of everything Launchway holds.", Renameable: true, MustNotBe: []string{"dashboard (for the first page)"}},
	{Key: "monitoring", Group: "platform", Aviation: f("Radar"), Plain: fv("Monitoring", "Monitoring", "", "", ""),
		Meaning: "Health, metrics and the map of how services talk to each other.", Renameable: true, MustNotBe: []string{"observability (in screens)"}},
	{Key: "notifications", Group: "platform", Aviation: fv("Comms", "Comms", "", "", ""), Plain: fv("Notifications", "Notifications", "", "", ""),
		Meaning: "Messages Launchway sends when something needs attention, and where they go.", Renameable: true},
	{Key: "registry", Group: "platform", Aviation: f("Depot"), Plain: f("Registry"),
		Meaning: "Launchway's built-in store for container images and charts.", Renameable: true, MustNotBe: []string{"repository (for the store)"}},
	{Key: "artifact", Group: "platform", Aviation: fv("Cargo", "Cargo", "", "", ""), Plain: f("Artifact"),
		Meaning: "A file or image a pipeline builds and keeps, such as a package or container image.", Renameable: true, MustNotBe: []string{"build output"}},
	{Key: "standards", Group: "platform", Aviation: fv("Standing orders", "Standing orders", "", "", ""), Plain: fv("Standards", "Standards", "", "", ""),
		Meaning: "The checks every repository gets without asking, and the gates that block a change that introduces a serious problem.", Renameable: true, MustNotBe: []string{"compliance rules", "default policy"}},
	{Key: "scan", Group: "platform", Aviation: f("Inspection"), Plain: f("Security scan"),
		Meaning: "The stage Standing orders add to every run: the security checks, then one verdict on what the change introduces.", Renameable: true, MustNotBe: []string{"scan job"}},
	{Key: "dependency_updates", Group: "platform", Aviation: fv("Maintenance", "Maintenance", "", "", ""), Plain: fv("Dependency updates", "Dependency updates", "", "", ""),
		Meaning: "Proposed updates to the libraries a project uses, opened as pull requests.", Renameable: true},
	{Key: "chaos_test", Group: "platform", Aviation: f("Drill"), Plain: f("Chaos test"),
		Meaning: "A planned failure, such as stopping a service, to prove the system recovers.", Renameable: true},
	{Key: "learn", Group: "platform", Aviation: f("Flight school"), Plain: f("Learn"),
		Meaning: "Short lessons that teach Launchway by doing.", Renameable: true},
	{Key: "first_run", Group: "platform", Aviation: f("In-processing"), Plain: f("Setup"),
		Meaning: "The guided setup the first time Launchway starts.", Renameable: true},
	{Key: "guide", Group: "platform", Aviation: f("Wingman"), Plain: f("Guide"),
		Meaning: "The helper panel beside every page: where you are and what you can do next.", Renameable: true, MustNotBe: []string{"assistant", "copilot", "chatbot", "bot"}},
	{Key: "logbook", Group: "platform", Aviation: f("Logbook"), Plain: f("Audit trail"),
		Meaning: "The append-only record of who did what, and when.", Renameable: true, MustNotBe: []string{"audit log"}},

	// roles
	{Key: "owner", Group: "roles", Aviation: f("Commander"), Plain: f("Owner"),
		Meaning: "Can do everything, including who may do what. The stored role is owner.", Renameable: true},
	{Key: "approver", Group: "roles", Aviation: f("Controller"), Plain: f("Approver"),
		Meaning: "Decides whether a change may go on. The stored role is approver.", Renameable: true},
	{Key: "developer", Group: "roles", Aviation: f("Pilot"), Plain: f("Developer"),
		Meaning: "Writes code and starts runs. The stored role is developer.", Renameable: true},
	{Key: "viewer", Group: "roles", Aviation: f("Observer"), Plain: f("Viewer"),
		Meaning: "Can read everything and change nothing. The stored role is viewer.", Renameable: true},

	// statuses: the plain word is always shown beside the themed one; not renameable
	{Key: "queued", Group: "statuses", Aviation: f("Holding"), Plain: f("Queued"), Meaning: "Waiting for a runner or a free slot."},
	{Key: "running", Group: "statuses", Aviation: f("Airborne"), Plain: f("Running"), Meaning: "Working right now."},
	{Key: "succeeded", Group: "statuses", Aviation: f("Mission complete"), Plain: f("Succeeded"), Meaning: "Finished, and everything passed."},
	{Key: "failed", Group: "statuses", Aviation: f("Failed"), Plain: f("Failed"), Meaning: "Finished, and something did not pass."},
	{Key: "canceled", Group: "statuses", Aviation: f("Scrubbed"), Plain: f("Canceled"), Meaning: "Stopped on purpose before it finished."},
	{Key: "rolled_back", Group: "statuses", Aviation: f("Returned to base"), Plain: f("Rolled back"), Meaning: "Went back to the previous version."},
	{Key: "waiting", Group: "statuses", Aviation: f("Waiting"), Plain: f("Waiting"), Meaning: "Paused until a person decides or a condition is met."},
}

// Vocabulary returns the table (a copy).
func Vocabulary() []Word { return slices.Clone(vocabulary) }

// VocabularyWord finds a word by key.
func VocabularyWord(key string) (Word, bool) {
	for _, w := range vocabulary {
		if w.Key == key {
			return w, true
		}
	}
	return Word{}, false
}

// TermKeys are the words an installation may rename.
var TermKeys = func() []string {
	var out []string
	for _, w := range vocabulary {
		if w.Renameable {
			out = append(out, w.Key)
		}
	}
	return out
}()

func (w Word) forms(set string) Forms {
	if set == "business" {
		return w.Plain
	}
	return w.Aviation
}

func (f Forms) plural() string {
	if f.Many != "" {
		return f.Many
	}
	return pluralOf(f.One)
}

// Words resolves words for one set with the installation's renames: the Go
// equivalent of the web helper. The zero value is the default set.
type Words struct{ t Terms }

// Words is the helper for these terms.
func (t Terms) Words() Words { return Words{t: t} }

// DefaultWords is the helper for the default (aviation) set.
func DefaultWords() Words { return Words{} }

func (w Words) set() string {
	if w.t.Set == "" {
		return TermSets[0]
	}
	return w.t.Set
}

// One is the word as a name, singular: "Sortie". An unknown key is returned readably.
func (w Words) One(key string) string {
	if v := w.t.Overrides[key]; v != "" {
		return v
	}
	if d, ok := VocabularyWord(key); ok {
		return d.forms(w.set()).One
	}
	k := strings.ReplaceAll(key, "_", " ")
	if k == "" {
		return k
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

// Many is the plural: "Sorties".
func (w Words) Many(key string) string {
	if v := w.t.Overrides[key+"_plural"]; v != "" {
		return v
	}
	if v := w.t.Overrides[key]; v != "" {
		return pluralOf(v)
	}
	if d, ok := VocabularyWord(key); ok {
		return d.forms(w.set()).plural()
	}
	return pluralOf(w.One(key))
}

// Plain is the plain word of the key whatever set is in use ("Run"), for a tooltip or a confirmation.
func (w Words) Plain(key string) string {
	if d, ok := VocabularyWord(key); ok {
		return d.Plain.One
	}
	return w.One(key)
}

// Verb is a verb form: form is "do" (launch), "did" (launched) or "doing" (launching).
// A word with no verb form gives its name, lower-cased.
func (w Words) Verb(key, form string) string {
	if d, ok := VocabularyWord(key); ok {
		fm := d.forms(w.set())
		var v string
		switch form {
		case "does":
			b := w.Verb(key, "do")
			if strings.HasSuffix(b, "s") || strings.HasSuffix(b, "x") || strings.HasSuffix(b, "ch") || strings.HasSuffix(b, "sh") {
				return b + "es"
			}
			return b + "s"
		case "did":
			v = fm.Did
		case "doing":
			v = fm.Doing
		default:
			v = fm.Do
		}
		if v != "" && w.t.Overrides[key] == "" {
			return v
		}
	}
	return strings.ToLower(w.One(key))
}

// mid is a word in the middle of a sentence. Product words stay capitalized as
// names in the aviation set; the plain set reads as ordinary English, except
// the level words, which stay names in both.
func (w Words) mid(key, s string) string {
	d, ok := VocabularyWord(key)
	if w.set() == "aviation" || (ok && d.Group == "levels") || s == "" {
		return s
	}
	if strings.ContainsAny(s[1:], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

var articleHole = regexp.MustCompile(`\b([Aa])n? (\{[A-Za-z_]+(?::\w+)?\})`)

// Say fills a sentence: {run} {runs} {Run} {deploy:do} {deploy:did} {deploy:doing} {run:plain}.
// An unknown name is left as written, so a sentence with braces for another reason survives.
func (w Words) Say(text string) string {
	// "a {environment}" becomes "an Airfield" or "a Station": the article follows the word that is shown.
	text = articleHole.ReplaceAllStringFunc(text, func(all string) string {
		m := articleHole.FindStringSubmatch(all)
		out, ok := w.fill(m[2][1 : len(m[2])-1])
		if !ok || out == "" {
			return all
		}
		n := ""
		if strings.ContainsRune("aeiouAEIOU", rune(out[0])) {
			n = "n"
		}
		return m[1] + n + " " + out
	})
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] != '{' {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := strings.IndexByte(text[i:], '}')
		if j < 0 {
			b.WriteString(text[i:])
			break
		}
		raw := text[i+1 : i+j]
		if out, ok := w.fill(raw); ok {
			b.WriteString(out)
		} else {
			b.WriteString(text[i : i+j+1])
		}
		i += j + 1
	}
	return b.String()
}

func (w Words) fill(raw string) (string, bool) {
	name, mod, _ := strings.Cut(raw, ":")
	if name == "" {
		return "", false
	}
	upper := name[0] >= 'A' && name[0] <= 'Z'
	key := strings.ToLower(name[:1]) + name[1:]
	known := func(k string) bool { _, ok := VocabularyWord(k); return ok }
	plural := false
	if !known(key) && strings.HasSuffix(key, "s") && known(strings.TrimSuffix(key, "s")) {
		key, plural = strings.TrimSuffix(key, "s"), true
	} else if !known(key) {
		return "", false
	}
	switch mod {
	case "do", "does", "did", "doing":
		v := w.Verb(key, mod)
		if upper {
			v = strings.ToUpper(v[:1]) + v[1:]
		}
		return v, true
	case "plain":
		return strings.ToLower(w.Plain(key)), true
	case "":
	default:
		return "", false
	}
	s := w.One(key)
	if plural {
		s = w.Many(key)
	}
	if upper {
		return s, true
	}
	return w.mid(key, s), true
}

// WebVocabulary is the table as the web app and the overview carry it: without the
// notes only the documentation tools read.
func WebVocabulary() []Word {
	out := Vocabulary()
	for i := range out {
		out[i].MustNotBe, out[i].Identifier = nil, ""
	}
	return out
}

// current holds the words of the installation this process serves, set whenever its configuration is composed,
// so a sentence built far from the settings (an API error, a notification) can say its words the way the
// installation chose them. Before any configuration is read it is the default set.
var current atomic.Pointer[Words]

// SetCurrentTerms records the installation's chosen set and renames.
func SetCurrentTerms(t Terms) { w := t.Words(); current.Store(&w) }

// CurrentWords is the helper for the installation's words.
func CurrentWords() Words {
	if w := current.Load(); w != nil {
		return *w
	}
	return DefaultWords()
}
