package engine

import (
	"strings"
	"testing"
)

const draftText = `pipeline: demo
steps:
  build: {image: x, run: make}
  deploy:
    image: x
    needs: build
    when: { branch: main }
    run: ./deploy.sh
  draft-check:
    image: x
    needs: build
    when: { event: [draft] }
    run: echo checking the draft
  publish:
    image: x
    needs: build
    secrets:
      TOKEN: { store: vault, key: apps/registry, field: token }
    run: ./publish.sh
`

var draftOnMain = Trigger{Ref: "refs/heads/main", Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567", Actor: "dana"}

func createDraft(t *testing.T, e *Engine, text string, secrets bool) string {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, text), Trigger: draftOnMain,
		Source: Source{Kind: "git", URL: "/srv/repo"}, Draft: &Draft{Text: text, Hash: "sha256:made-up", Secrets: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// A draft run keeps the text it ran and a hash of it, worked out by the
// engine, and is marked as a draft wherever runs are listed.
func TestDraftRunKeepsItsText(t *testing.T) {
	e, _ := newEngine(t)
	id := createDraft(t, e, draftText, false)
	plain := create(t, e, linear, Trigger{Event: "manual"})

	sum, err := e.Summary(id)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Draft || sum.Event != "draft" || sum.Actor != "dana" || sum.PipelineHash != DraftHash(draftText) {
		t.Fatalf("summary %+v", sum)
	}
	if !strings.HasPrefix(sum.PipelineHash, "sha256:") || len(sum.PipelineHash) != len("sha256:")+64 {
		t.Fatalf("hash %q", sum.PipelineHash)
	}
	list, _ := e.ListRuns(RunFilter{})
	for _, r := range list {
		if r.Draft != (r.ID == id) {
			t.Fatalf("run %s listed with draft=%v", r.ID, r.Draft)
		}
	}
	d, isDraft, err := e.DraftText(id)
	if err != nil || !isDraft || d.Text != draftText || d.Hash != sum.PipelineHash || d.Secrets {
		t.Fatalf("draft text: %v %v %+v", err, isDraft, d)
	}
	if _, isDraft, err := e.DraftText(plain); err != nil || isDraft {
		t.Fatalf("a run from Git reads as a draft: %v %v", isDraft, err)
	}
	if _, _, err := e.DraftText("run_nope"); err == nil {
		t.Fatal("a missing run has draft text")
	}
	// The event log is the record: the stored state agrees with it.
	st, _ := e.State(id)
	if st.Draft == nil || st.Draft.Text != draftText {
		t.Fatalf("state %+v", st.Draft)
	}

	// A run can only be a draft with its text, and a push never makes one.
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, linear), Trigger: Trigger{Event: "draft"}}); err == nil {
		t.Fatal("a draft run without text was created")
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, linear), Trigger: Trigger{Event: "push"},
		Draft: &Draft{Text: linear}, Push: &RefChange{Ref: "refs/heads/main", New: "abc"}}); err == nil {
		t.Fatal("a push created a draft run")
	}
}

// In a draft, a step guarded by when runs only if its event list names
// draft; a step that declares secrets fails before any runner sees it, and
// no job carries secret references.
func TestDraftConditionsAndSecrets(t *testing.T) {
	e, _ := newEngine(t)
	id := createDraft(t, e, draftText, false)
	j := take(t, e)
	if j.StepID != "build" || len(j.Secrets) != 0 || j.Env["LAUNCHWAY_EVENT"] != "draft" {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, nil)
	got := statuses(t, e, id)
	want(t, got, "deploy", "skipped", "draft-check", "queued", "publish", "failed")
	pub := stepOf(t, e, id, "publish")
	if !strings.Contains(pub.Error, "draft runs of demo may not read secrets") || !strings.Contains(pub.Error, "TOKEN") ||
		pub.Attempt != 0 || pub.Runner != "" {
		t.Fatalf("publish %+v", pub)
	}
	j = take(t, e)
	if j.StepID != "draft-check" || j.Secrets != nil {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, nil)
	none(t, e)
	want(t, statuses(t, e, id), "run", "failed")

	// Resuming as the setting still says asks nothing of a runner.
	if err := e.ResumeDraft(id, "dana", false); err != nil {
		t.Fatal(err)
	}
	none(t, e)
	want(t, statuses(t, e, id), "run", "failed", "publish", "failed")

	// Once the project lets drafts read secrets, a resume hands the step,
	// with its references, to a runner.
	if err := e.ResumeDraft(id, "dana", true); err != nil {
		t.Fatal(err)
	}
	j = take(t, e)
	if j.StepID != "publish" || j.Secrets["TOKEN"].Key != "apps/registry" {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded")

	// ResumeDraft refuses a run that is not a draft.
	plain := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: a}\n", Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	if err := e.ResumeDraft(plain, "dana", true); err == nil {
		t.Fatal("resumed a plain run as a draft")
	}
}

// With secrets allowed, a draft's steps get their references like any run.
func TestDraftWithSecretsAllowed(t *testing.T) {
	e, _ := newEngine(t)
	createDraft(t, e, "pipeline: demo\nsteps:\n  a:\n    image: x\n    secrets: { T: { store: env, key: K } }\n    run: a\n", true)
	if j := take(t, e); j.Secrets["T"].Key != "K" {
		t.Fatalf("job %+v", j)
	}
}

// A manual or pushed run is not a draft: steps guarded by when run as
// before.
func TestConditionsOutsideDrafts(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, draftText, Trigger{Event: "manual", Branch: "main"})
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "deploy", "queued", "draft-check", "skipped", "publish", "queued")
}

// A failed step keeps the last lines of its output, cleaned up, until it is
// run again.
func TestFailureExcerpt(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: a, retry: 1}\n", Trigger{Event: "manual"})
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, "line "+strings.Repeat("x", i))
	}
	raw := "\x1b[31merror:\x1b[0m connection refused\r\n\n\t\x07" + strings.Join(lines, "\n") + "\npassword was *** \x1b]0;title\x07\n"
	j := take(t, e)
	one := 1
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", ExitCode: &one, Error: "exit code 1", Excerpt: raw}); err != nil {
		t.Fatal(err)
	}
	// It is retried, so the step is queued again; the excerpt went with the
	// failed attempt's event.
	if s := stepOf(t, e, id, "a"); s.Status != StepQueuedS || s.Excerpt != "" {
		t.Fatalf("after the retry was queued: %+v", s)
	}
	j = take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", ExitCode: &one, Error: "exit code 1", Excerpt: raw}); err != nil {
		t.Fatal(err)
	}
	s := stepOf(t, e, id, "a")
	got := strings.Split(s.Excerpt, "\n")
	if len(got) != MaxExcerptLines || got[len(got)-1] != "password was ***" || got[0] != "line "+strings.Repeat("x", 6) {
		t.Fatalf("excerpt %q", s.Excerpt)
	}
	if strings.ContainsAny(s.Excerpt, "\x1b\x07\r\t") {
		t.Fatalf("control characters kept: %q", s.Excerpt)
	}
	st, _ := e.State(id)
	if st.Steps["a"].Excerpt != s.Excerpt {
		t.Fatalf("the table says %q, the events %q", s.Excerpt, st.Steps["a"].Excerpt)
	}
	// A successful attempt has none, and a resume clears it.
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, id, "a"); s.Excerpt != "" {
		t.Fatalf("after resume: %q", s.Excerpt)
	}
	ok(t, e, take(t, e), nil)
	if s := stepOf(t, e, id, "a"); s.Excerpt != "" || s.Status != StepSucceeded {
		t.Fatalf("after success: %+v", s)
	}
}

func TestCleanExcerptLimits(t *testing.T) {
	long := strings.Repeat("é", MaxExcerptBytes) // two bytes each
	got := CleanExcerpt(long)
	if len(got) > MaxExcerptBytes+len("…") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "é") {
		t.Fatalf("%d bytes: %q…", len(got), got[:20])
	}
	if CleanExcerpt("\n \n\t\n") != "" {
		t.Fatal("blank lines kept")
	}
	if got := CleanExcerpt("bad \xff byte"); got != "bad ? byte" {
		t.Fatalf("%q", got)
	}
}
