// Package engine runs pipelines durably.
//
// Every change to a run is an event appended to the database. A run's state
// is rebuilt from its events, so the engine can stop at any moment (crash,
// upgrade, power cut) and carry on from exactly where it was. Steps are handed
// to runners on leases that the runner must keep renewing; a step whose
// runner disappears is queued again.
//
// Several engines (one per server) may share a PostgreSQL database. Each
// run's events are numbered (run_seq), so when two servers change the same
// run at once only one succeeds and the other retries against the new state.
// Leases live in the database, so any server can serve any runner.
package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/hub"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrLeaseLost = errors.New("the lease is no longer valid")
	ErrForbidden = errors.New("not allowed")
	ErrInvalid   = errors.New("invalid")
	ErrClosed    = errors.New("the engine is shutting down")
	// ErrNeedsNewer means a run has events, or a pipeline, written by a
	// newer version of Launchway than this server: only a newer server may
	// change it (see RunState.NotUnderstood).
	ErrNeedsNewer = errors.New("needs a newer Launchway server")
	// ErrAlreadyClaimed means another server has already acted on a change
	// to a ref (see RefChange).
	ErrAlreadyClaimed = errors.New("another server has already handled this change")
)

// Options configure an Engine.
type Options struct {
	Database string        // a SQLite file, or a postgres:// URL that several servers share
	LeaseTTL time.Duration // how long a runner may go silent; default 30s
	MaxLost  int           // times a step is requeued after losing its runner; default 3
	// RunnerTimeout is how long a runner may go unseen before another runner
	// may take over its runs (see Lease); default 3 × LeaseTTL.
	RunnerTimeout time.Duration
	// ConnectWait is how long Open keeps trying a database that does not
	// answer, for example because it is still starting; default 0, one try.
	ConnectWait time.Duration
	Hub         *hub.Hub // live updates; optional
	Logger      *slog.Logger
	Now         func() time.Time // for tests
	// AllowSelfApproval says whether the person who started a run may
	// approve its approval steps. Nil means yes, because until there is
	// sign-in everyone acts as the same configured user.
	AllowSelfApproval *bool
	// Tests limits what is kept of test reports; zero fields take the
	// defaults (see TestLimits).
	Tests TestLimits
	// Artifacts configures where artifacts are kept and their limits;
	// Findings limits what is kept of security findings and SBOMs.
	Artifacts ArtifactOptions
	Findings  FindingLimits
	// Environments are where deploy steps may deploy (schema 11). Every
	// server sharing a database must be given the same ones.
	Environments []Environment

	// schema, for tests, makes the engine behave as a build that knows only
	// the first schema migrations and their events; 0 means every one (or
	// what the build's schemaLimit says).
	schema int
}

// Engine is safe for concurrent use.
type Engine struct {
	db            *dbx
	hub           *hub.Hub
	log           *slog.Logger
	ttl           time.Duration
	runnerTimeout time.Duration
	maxLost       int
	now           func() time.Time
	selfOK        bool   // AllowSelfApproval
	schema        int    // the newest schema this build knows: its tables, columns and events
	instance      string // tags this process's notifications, so it can ignore its own
	channel       string // the PostgreSQL notification channel
	tests         TestLimits
	arts          ArtifactOptions
	finds         FindingLimits
	envs          map[string]Environment

	// deployKick wakes the reaper to hand free locks to waiting deploy steps
	// at once (see kickDeploys).
	deployKick chan struct{}
	// published throttles live updates of observations (see Observe).
	pubMu     sync.Mutex
	published map[string]int64

	// writeMu serialises writes on SQLite, which allows one writer at a time
	// anyway; waiting here is fairer and cheaper than SQLite's busy loop. On
	// PostgreSQL it is not used: writers to the same run are kept apart by
	// run_seq, and writers to different runs do not need to be.
	writeMu sync.Mutex

	wakeMu sync.Mutex
	wake   chan struct{}

	stop      chan struct{}
	cancelBg  context.CancelFunc
	bg        sync.WaitGroup // the reaper and the notification listener
	closeOnce sync.Once
	closeErr  error
}

// RunnerInfo identifies a runner asking for work.
type RunnerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Version  string `json:"version"`
	// Features are what the runner can do beyond the basics, separated by
	// spaces, such as FeatureArtifacts; steps that need a feature go only
	// to runners that have it. (A string keeps RunnerInfo comparable.)
	Features string `json:"features,omitempty"`
	// Targets, for a deploy runner, are the targets its configuration
	// serves. The server only gives it work for those its pool may serve.
	Targets []string `json:"targets,omitempty"`
	// Pool is the runner pool its token belongs to, set by the server from
	// the token, never by the runner.
	Pool string `json:"-"`
}

// Job is everything a runner needs to execute one step attempt.
type Job struct {
	LeaseID   string            `json:"lease_id"`
	LeaseTTL  pipeline.Duration `json:"lease_ttl"`
	RunID     string            `json:"run_id"`
	Project   string            `json:"project"`
	RunNumber int               `json:"run_number"`
	Pipeline  string            `json:"pipeline"`
	StepID    string            `json:"step_id"`
	Attempt   int               `json:"attempt"`
	Image     string            `json:"image"`
	Script    string            `json:"script"`
	Env       map[string]string `json:"env"`
	// Secrets are references only. The runner looks the values up, adds
	// them to the environment and masks them in the step's output.
	Secrets    map[string]pipeline.SecretRef `json:"secrets,omitempty"`
	Timeout    pipeline.Duration             `json:"timeout"`
	Source     Source                        `json:"source"`
	SHA        string                        `json:"sha,omitempty"`
	Container  string                        `json:"container"`  // which of Containers the step runs in
	Containers []pipeline.Container          `json:"containers"` // every container the run needs, services included
	// Workspace names the run's workspace on this runner: the same for every
	// step the runner takes while it holds the run, and new when it takes
	// the run over from another runner (see Lease).
	Workspace string `json:"workspace"`
	// TakenOverFrom is the runner that held the run until it stopped
	// responding or left, if this step moved the run to this runner. Files
	// that the run's earlier steps wrote are not in the new workspace.
	TakenOverFrom string `json:"taken_over_from,omitempty"`
	// Library is the library of the template the step came from, if any:
	// its files are what LAUNCHWAY_LIB points at. Source.Libraries says
	// where every library of the run is fetched from.
	Library string `json:"library,omitempty"`
	// Reports, for a step that declares test reports, says which files the
	// runner collects once the step ends, and how much the server accepts.
	// A runner that does not know it collects nothing.
	Reports *JobReports `json:"reports,omitempty"`
	// Artifacts says what the runner keeps once the step has run, and Fetch
	// what it puts in the workspace before (schema 12).
	Artifacts *JobArtifacts `json:"artifacts,omitempty"`
	Fetch     []JobFetch    `json:"fetch,omitempty"`
	// Deploy, for a deploy or rollback step, is the action a deploy runner
	// does. Only deploy runners lease such steps.
	Deploy *DeployJob `json:"deploy,omitempty"`
	// ImageBuild, for an image step, is what to build (schema 15). Only
	// runners with FeatureImages lease such steps.
	ImageBuild *ImageJob `json:"image_build,omitempty"`
}

// JobReports is what a runner collects after a step.
type JobReports struct {
	Tests []string `json:"tests"` // globs, relative to the workspace
	// MaxBytes is how many bytes of report files the server accepts for
	// one attempt; the runner sends no more.
	MaxBytes int64 `json:"max_bytes"`
	// Findings are globs of SARIF files, and FindingsMaxBytes how many
	// bytes of them the server accepts (schema 12).
	Findings         []string `json:"findings,omitempty"`
	FindingsMaxBytes int64    `json:"findings_max_bytes,omitempty"`
}

// Result is how a step attempt ended, as reported by its runner.
type Result struct {
	Outcome  string            `json:"outcome"` // succeeded, failed, canceled, or lost (the machinery failed; run it again)
	ExitCode *int              `json:"exit_code,omitempty"`
	Error    string            `json:"error,omitempty"`
	Outputs  map[string]string `json:"outputs,omitempty"`
	// Excerpt is, for a failed step, the last few lines it wrote: on stderr
	// if it wrote any, else on stdout. The runner masks secrets in it as in
	// the log; the server keeps at most MaxExcerptBytes of it.
	Excerpt string `json:"excerpt,omitempty"`
	// Tests, for a step that declares test reports, is what the runner
	// collected once the step ended, whether it passed or failed: nil if it
	// collected nothing (a runner that does not know reports, or one that is
	// shutting down).
	Tests *CollectedReports `json:"tests,omitempty"`
	// Findings, for a step that declares reports.findings, are the SARIF
	// files the runner collected, like Tests (schema 12).
	Findings *CollectedReports `json:"findings,omitempty"`
	// Deploy, for a deploy runner's action, is what it did.
	Deploy *DeployFacts `json:"deploy,omitempty"`
	// Image, for an image step, is what it made (schema 15): also when it
	// failed after building (a scan that failed it, a push that failed).
	Image *ImageRecord `json:"image,omitempty"`
}

// CollectedReports are the report files a runner found after a step, and
// what it has to say about collecting them.
type CollectedReports struct {
	Files []ReportFile `json:"files"`
	// Notes say what was not collected and why: globs that matched no
	// file, files cut short, limits reached. The runner also writes them in
	// the step's log.
	Notes []string `json:"notes,omitempty"`
}

// ReportFile is one report file, as the runner read it.
type ReportFile struct {
	Path string `json:"path"` // relative to the workspace
	Data []byte `json:"data"`
	// Size is the file's size when the runner knows it. Cut says the runner
	// kept only the start of the file, len(Data) bytes, because of its
	// limits.
	Size int64 `json:"size,omitempty"`
	Cut  bool  `json:"cut,omitempty"`
}

// LogLine is one line of a step's output.
type LogLine struct {
	Seq    int    `json:"seq"`
	At     int64  `json:"at"`
	Stream string `json:"stream"` // stdout, stderr or system
	Text   string `json:"text"`
}

// Open opens the database, recovers any steps that were in flight, and starts
// watching leases.
func Open(o Options) (*Engine, error) {
	if o.LeaseTTL == 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.MaxLost == 0 {
		o.MaxLost = 3
	}
	if o.RunnerTimeout == 0 {
		o.RunnerTimeout = 3 * o.LeaseTTL
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Hub == nil {
		o.Hub = hub.New()
	}
	known, err := knownSchema()
	if err != nil {
		return nil, err
	}
	tests, err := o.Tests.withDefaults()
	if err != nil {
		return nil, err
	}
	arts, err := o.Artifacts.withDefaults()
	if err != nil {
		return nil, err
	}
	finds, err := o.Findings.withDefaults()
	if err != nil {
		return nil, err
	}
	if o.schema > 0 {
		known = min(o.schema, len(migrations))
	}
	envs, err := checkEnvironments(o.Environments)
	if err != nil {
		return nil, err
	}
	if len(envs) > 0 && known < schemaDeploy {
		return nil, fmt.Errorf("environments need schema %d; this build knows %d", schemaDeploy, known)
	}
	db, _, err := openDB(o.Database, o.ConnectWait, known, o.Logger)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		db: db, hub: o.Hub, log: o.Logger, ttl: o.LeaseTTL, runnerTimeout: o.RunnerTimeout, maxLost: o.MaxLost, now: o.Now,
		schema:     known,
		tests:      tests,
		arts:       arts,
		finds:      finds,
		envs:       envs,
		deployKick: make(chan struct{}, 1),
		published:  map[string]int64{},
		selfOK:     o.AllowSelfApproval == nil || *o.AllowSelfApproval,
		instance:   ids.New("srv"),
		wake:       make(chan struct{}),
		stop:       make(chan struct{}),
	}
	if db.pg {
		var schema string
		if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
			db.Close()
			return nil, err
		}
		e.channel = channelFor(schema)
	}
	if err := e.recoverLeases(); err != nil {
		db.Close()
		return nil, err
	}
	if err := e.checkArtifactStore(); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancelBg = cancel
	if db.pg {
		// Listen before serving anything, so no notification is missed.
		conn, err := e.startListening(ctx)
		if err != nil {
			cancel()
			db.Close()
			return nil, err
		}
		e.bg.Add(1)
		go e.listen(ctx, conn)
	}
	e.bg.Add(1)
	go e.reaper(ctx)
	return e, nil
}

// Close stops the engine. Runs in flight stay recorded and continue when it
// (or another server sharing the database) carries on.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		close(e.stop)
		e.cancelBg()
		e.bg.Wait()
		e.notify()
		unlock := e.writeLock()
		defer unlock()
		e.closeErr = e.db.Close()
	})
	return e.closeErr
}

// Hub returns the live update hub.
func (e *Engine) Hub() *hub.Hub { return e.hub }

// Ping checks that the database answers.
func (e *Engine) Ping(ctx context.Context) error {
	var one int
	return e.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// Schema compares the schema this build knows with the database's, as the
// database says now: another server may have migrated it since this one
// started.
func (e *Engine) Schema(ctx context.Context) (SchemaInfo, error) {
	info, err := readSchema(ctxQuerier{e.db, ctx})
	info.Code = e.schema
	return info, err
}

// ctxQuerier runs QueryRow with a context.
type ctxQuerier struct {
	d   *dbx
	ctx context.Context
}

func (q ctxQuerier) QueryRow(s string, a ...any) *sql.Row {
	return q.d.QueryRowContext(q.ctx, rebind(q.d.pg, s), a...)
}

// knows reports whether this build knows schema v, and so its tables,
// columns and events.
func (e *Engine) knows(v int) bool { return e.schema >= v }

// writeLock takes the SQLite write lock; on PostgreSQL it does nothing.
func (e *Engine) writeLock() (unlock func()) {
	if e.db.pg {
		return func() {}
	}
	e.writeMu.Lock()
	return e.writeMu.Unlock
}

// recoverLeases prepares the leases of steps that were in flight when the
// last server stopped. If no server has been alive for a while, every lease
// gets a fresh period: a runner that is still working carries on, and one
// that is gone lets its lease expire so the step is queued again. If another
// server is running, its leases are left exactly as they are.
func (e *Engine) recoverLeases() error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := e.now()
	until := now.Add(e.ttl).UnixMilli()
	var v string
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'alive_at'`).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	alive, _ := strconv.ParseInt(v, 10, 64)
	// Leases carried over from schema 1 have no expiry yet.
	if _, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE expires_at = 0`, until); err != nil {
		return err
	}
	if now.UnixMilli()-alive > (e.ttl / 2).Milliseconds() {
		res, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE expires_at < ?`, until, until)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			e.log.Info("recovered steps that were in flight", "count", n)
		}
	}
	if err := setAlive(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// setAlive records that a server was running at t.
func setAlive(q interface {
	Exec(string, ...any) (sql.Result, error)
}, t time.Time) error {
	_, err := q.Exec(`INSERT INTO meta(key, value) VALUES('alive_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(t.UnixMilli(), 10))
	return err
}

// ---- commands ----

// NewRun describes a run to create.
type NewRun struct {
	Project  string
	Pipeline *pipeline.Pipeline
	Trigger  Trigger
	Source   Source
	// Draft, if set, makes this a draft run of pipeline text that is not in
	// Git. Its trigger event is always draft, and its hash is worked out
	// here from the text.
	Draft *Draft
	// Push, if set, is the change to a ref that this run is for. The change
	// is claimed in the same transaction that creates the run, and a
	// "started" decision is recorded; if another server claimed it first,
	// CreateRun returns ErrAlreadyClaimed and creates nothing.
	Push *RefChange
	// Note is added to the "started" decision of a push, to say more about
	// what started, such as where the pipeline came from.
	Note string
	// PullRequestSecrets, for a run whose trigger event is pull_request,
	// says whether its steps may read secrets. Such runs come from code
	// anyone who can open a pull request may have written.
	PullRequestSecrets bool
	// Fire, for a run of event schedule, is the firing it is for, claimed
	// with the run like Push; Delivery, for a run of event webhook, is the
	// caller's idempotency key, if it sent one. CreateRun returns
	// ErrAlreadyClaimed if another server or call claimed it first.
	Fire     *Firing
	Delivery *Delivery
}

// checkPullRequest checks the trigger of a pull-request run.
func checkPullRequest(t Trigger) error {
	if t.PullRequest <= 0 || t.Ref != PullRequestRef(t.PullRequest) || t.Branch != "" || t.Tag != "" {
		return fmt.Errorf("%w: a pull-request run needs the pull request's number and its ref, refs/pull/<number>/head", ErrConflict)
	}
	return nil
}

// PullRequestRef is the ref a Git host keeps a pull request's head at.
func PullRequestRef(number int) string { return "refs/pull/" + strconv.Itoa(number) + "/head" }

// checkLibraries checks that a pipeline is ready to run and that the source
// says where to fetch every library it was expanded from, at the same commit.
func checkLibraries(p *pipeline.Pipeline, src Source) error {
	if !p.Expanded() {
		return fmt.Errorf("%w: the pipeline uses templates that have not been expanded; "+
			"templates come from libraries the server knows, so run it on a server", ErrConflict)
	}
	for _, l := range p.Libraries {
		found := false
		for _, ls := range src.Libraries {
			if ls.Name == l.Name {
				found = ls.Commit == l.Commit && pipeline.IsCommitID(ls.Commit) && ls.URL != ""
			}
		}
		if !found {
			return fmt.Errorf("%w: the source does not say where to fetch library %s at %s", ErrConflict, l.Name, l.Commit)
		}
	}
	if len(src.Libraries) > 0 && src.Kind != "git" {
		return fmt.Errorf("%w: libraries are only for runs from a Git repository", ErrConflict)
	}
	return nil
}

// CreateRun records a new run and queues the steps that can start.
func (e *Engine) CreateRun(nr NewRun) (*RunSummary, error) {
	if nr.Project == "" || nr.Pipeline == nil || len(nr.Pipeline.Steps) == 0 {
		return nil, fmt.Errorf("%w: a run needs a project and a pipeline with steps", ErrConflict)
	}
	if nr.Trigger.Event == "" {
		nr.Trigger.Event = pipeline.EventManual
	}
	if err := checkLibraries(nr.Pipeline, nr.Source); err != nil {
		return nil, err
	}
	// What each schema brought, in order: a server, or a build limited to
	// an older schema for testing, refuses what it does not know.
	if nr.Pipeline.UsesSchema10() && !e.knows(schemaTests) {
		// A release that old cannot even read the pipeline; a build limited
		// to it for testing refuses in the same place.
		return nil, fmt.Errorf("%w: this server does not know test reports (schema %d)", ErrConflict, schemaTests)
	}
	if nr.Pipeline.UsesSchema11() {
		if !e.knows(schemaDeploy) {
			return nil, fmt.Errorf("%w: this server does not know deploy steps (schema %d)", ErrConflict, schemaDeploy)
		}
		if ps := e.CheckDeploys(nr.Project, nr.Pipeline); len(ps) > 0 {
			return nil, ps
		}
	}
	if nr.Pipeline.UsesSchema12() && !e.knows(schemaArtifacts) {
		return nil, fmt.Errorf("%w: this server does not know artifacts, fetch or findings (schema %d)", ErrConflict, schemaArtifacts)
	}
	if nr.Pipeline.UsesSchema15() && !e.knows(schemaImages) {
		return nil, fmt.Errorf("%w: this server does not know image steps or deploy changes that pin a digest (schema %d)", ErrConflict, schemaImages)
	}
	if err := e.checkNewRun(&nr); err != nil { // schema 13 and the run's inputs
		return nil, err
	}
	var draft *Draft
	switch {
	case nr.Draft != nil:
		if nr.Push != nil {
			return nil, fmt.Errorf("%w: a draft run is never started by a push", ErrConflict)
		}
		draft = &Draft{Text: nr.Draft.Text, Hash: DraftHash(nr.Draft.Text), Secrets: nr.Draft.Secrets}
		nr.Trigger.Event = pipeline.EventDraft
	case nr.Trigger.Event == pipeline.EventDraft:
		return nil, fmt.Errorf("%w: a draft run needs the text it runs", ErrConflict)
	}
	var prSecrets *bool
	if nr.Trigger.Event == pipeline.EventPullRequest {
		if !e.knows(schemaPullRequests) {
			return nil, fmt.Errorf("%w: this server does not know pull-request runs (schema %d)", ErrConflict, schemaPullRequests)
		}
		if err := checkPullRequest(nr.Trigger); err != nil {
			return nil, err
		}
		// Servers older than schema 9 do not know on.pull_request, so they
		// leave such a run alone instead of running it with secrets.
		if !nr.Pipeline.On.MatchPullRequest() {
			return nil, fmt.Errorf("%w: a pull-request run's pipeline must ask for pull requests (on: { pull_request: {} })", ErrConflict)
		}
		prSecrets = &nr.PullRequestSecrets
	}
	runID := ids.New("run")
	trig, src := nr.Trigger, nr.Source
	// Superseding: a new run started by a push or a pull request cancels
	// the older ones of the same ref, in the transaction that creates it.
	supersede := nr.Pipeline.Supersede && draft == nil && e.knows(schemaPullRequests) &&
		(trig.Event == pipeline.EventPush || trig.Event == pipeline.EventPullRequest) && trig.Ref != ""
	err := e.runTx(runID, true, func(c *cmd) error {
		if nr.Push != nil {
			claimed, err := claimRef(c.tx, nr.Project, *nr.Push)
			if err != nil {
				return err
			}
			if !claimed {
				return ErrAlreadyClaimed
			}
		}
		// Take the next number from the project's counter. The counter row
		// is locked until this transaction ends, so servers creating runs
		// for one project at the same moment queue up briefly instead of
		// colliding, and a rolled-back creation leaves no gap.
		var number int
		if err := c.tx.QueryRow(`INSERT INTO run_counters(project, last) VALUES(?, 1)
			ON CONFLICT(project) DO UPDATE SET last = run_counters.last + 1 RETURNING last`, nr.Project).Scan(&number); err != nil {
			return err
		}
		if err := c.claimNewRun(&nr, number); err != nil {
			return err
		}
		c.emit(RunCreated, Payload{Project: nr.Project, Number: number, Pipeline: nr.Pipeline, Trigger: &trig, Source: &src, Draft: draft,
			PullRequestSecrets: prSecrets})
		c.advance()
		var superseded []int
		if supersede {
			var err error
			if superseded, err = c.supersede(nr.Project, trig.Ref, number); err != nil {
				return err
			}
		}
		if nr.Push != nil {
			detail := fmt.Sprintf("started run #%d", number)
			if len(superseded) > 0 {
				nums := make([]string, len(superseded))
				for i, n := range superseded {
					nums[i] = "#" + strconv.Itoa(n)
				}
				detail += ", superseding " + strings.Join(nums, ", ")
			}
			return insertDecision(c.tx, nr.Project, PushDecision{At: c.now, Ref: nr.Push.Ref, SHA: nr.Push.New,
				Outcome: "started", Detail: strings.TrimSpace(detail + " " + nr.Note), RunID: runID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if nr.Push != nil {
		e.pruneDecisions(nr.Project)
	}
	return e.runSummary(runID)
}

// supersede cancels the runs of a project's ref that a new run, number,
// supersedes: those still queued or running that were started by a push or
// a pull request and are not drafts or re-runs. It runs in the transaction
// that creates the new run, after the run's number was taken: the project's
// counter row is then locked until the transaction ends, so a run created
// for the project at the same moment on another server is either seen here
// or sees this one. Each older run gets a run.cancel_requested event in the
// same transaction; if another server changes one of them first, the whole
// creation is tried again. It returns the numbers of the runs it cancelled.
func (c *cmd) supersede(project, ref string, number int) ([]int, error) {
	rows, err := c.tx.Query(`SELECT id FROM runs WHERE project = ? AND ref = ? AND event IN (?, ?) AND status IN (?, ?)
		AND cancel_requested = 0 AND draft = 0 AND rerun_of = '' AND number < ? AND min_schema <= ? ORDER BY number`,
		project, ref, pipeline.EventPush, pipeline.EventPullRequest, RunQueued, RunRunning, number, c.e.schema)
	if err != nil {
		return nil, err
	}
	var older []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		older = append(older, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var done []int
	for _, id := range older {
		err := c.alsoRun(id, func(o *cmd) error {
			// The events decide, not the row read above.
			st := o.st
			// A run that holds an open deployment finishes it; the newer run
			// waits for the environment's lock.
			if st.holdsDeployment() {
				return nil
			}
			if st.Status.Terminal() || st.CancelRequested || st.Draft != nil || st.Rerun != nil || st.Project != project ||
				st.Trigger.Ref != ref || (st.Trigger.Event != pipeline.EventPush && st.Trigger.Event != pipeline.EventPullRequest) {
				return nil
			}
			o.emit(RunCancelRequested, Payload{Reason: fmt.Sprintf("superseded by run #%d", number),
				SupersededBy: &Superseder{Run: c.runID, Number: number}})
			o.advance()
			done = append(done, st.Number)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return done, nil
}

// alsoRun changes another run in the same transaction as this command: fn
// sees that run's current state and what it emits is stored with this
// command's events, all or nothing. A run this server does not fully
// understand is left alone.
func (c *cmd) alsoRun(runID string, fn func(o *cmd) error) error {
	evs, runSeq, err := loadEvents(c.tx, runID)
	if err != nil {
		return err
	}
	st := c.e.fold(evs)
	if st == nil || needsNewer(st) != nil {
		return nil
	}
	o := &cmd{e: c.e, tx: c.tx, runID: runID, st: st, runSeq: runSeq, now: c.now, changed: map[string]bool{}, schemaBefore: st.Schema}
	if err := fn(o); err != nil {
		return err
	}
	if o.err != nil {
		return o.err
	}
	if len(o.events) == 0 {
		return nil
	}
	if err := o.persist(); err != nil {
		return err
	}
	c.others = append(c.others, o)
	return nil
}

// Cancel asks a run to stop. Steps not yet running are canceled at once;
// running steps are stopped by their runners, which learn of it from their
// next heartbeat.
func (e *Engine) Cancel(runID, actor string) error {
	return e.withRun(runID, func(c *cmd) error {
		if c.st.Status.Terminal() {
			return fmt.Errorf("%w: the run has already finished", ErrConflict)
		}
		if c.st.CancelRequested {
			return nil
		}
		open := []string(nil)
		if c.e.knows(schemaControl) {
			open = c.st.openDeployments()
		}
		c.emit(RunCancelRequested, Payload{Actor: actor})
		c.advance()
		c.abortOnCancel(open, actor)
		return nil
	})
}

// Resume restarts a failed or canceled run from the steps that did not
// succeed. Steps that succeeded are not run again.
func (e *Engine) Resume(runID, actor string) error {
	return e.resume(runID, actor, nil)
}

// ResumeDraft resumes a draft run, saying again whether its steps may read
// secrets, as the configuration now says. It refuses a run that is not a
// draft.
func (e *Engine) ResumeDraft(runID, actor string, secrets bool) error {
	return e.resume(runID, actor, &secrets)
}

func (e *Engine) resume(runID, actor string, draftSecrets *bool) error {
	return e.withRun(runID, func(c *cmd) error {
		if c.st.Status != RunFailed && c.st.Status != RunCanceled {
			return fmt.Errorf("%w: only a failed or canceled run can be resumed", ErrConflict)
		}
		if draftSecrets != nil && c.st.Draft == nil {
			return fmt.Errorf("%w: run %s is not a draft run", ErrConflict, runID)
		}
		if id := c.st.openedDeployment(); id != "" {
			return fmt.Errorf("%w: %s made a deployment in this run, and deployments are not resumed: "+
				"start a new run, or re-run this one from %s", ErrConflict, id, id)
		}
		c.emit(RunResumed, Payload{Actor: actor, DraftSecrets: draftSecrets})
		for _, id := range c.st.Order {
			s := c.st.Steps[id]
			if s.Status == StepFailed || s.Status == StepCanceledS || (s.Status == StepSkippedS && s.SkipKind == "upstream") {
				c.emit(StepReset, Payload{Step: id})
			}
		}
		c.resetAfterUpstream()
		c.advance()
		return nil
	})
}

// recheckEvery bounds how long a waiting Lease relies on notifications from
// other servers alone; a missed notification costs at most this long.
const recheckEvery = 2 * time.Second

// Lease hands the oldest queued step a runner may take to it, waiting up to
// wait for one to become available. It returns nil, nil if there is none. It
// stops waiting when ctx ends, for example because the runner disconnected.
//
// The steps of a run share a workspace, which lives on one runner. So once a
// runner has taken a step of a run, it holds the run: the run's later steps
// are given only to it, for as long as it is seen (it leases, heartbeats or
// sends logs) at least every RunnerTimeout. After that, or once it has said
// it is leaving, any runner may take the run over and carries on in a fresh
// workspace. All of this is decided from the database, so it holds however
// many servers there are.
//
// Steps of runs nobody holds go to any runner, but a runner that is already
// working leaves them to a runner that is doing nothing, if one has been
// seen within the lease TTL, so that runs spread across runners.
func (e *Engine) Lease(ctx context.Context, r RunnerInfo, wait time.Duration) (*Job, error) {
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		select {
		case <-e.stop:
			return nil, ErrClosed
		default:
		}
		e.touchRunner(r)
		job, err := e.tryLease(r)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(left)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

func (e *Engine) tryLease(r RunnerInfo) (*Job, error) {
	now := e.now()
	ownOnly, err := e.leaveNewRunsToIdle(r.ID, now)
	if err != nil {
		return nil, err
	}
	// Queued steps of runs this runner holds and, unless it should leave
	// them to others, of runs no live runner holds. The transaction below
	// checks again, against the run's current state.
	where, args := `runs.runner = ?`, []any{r.ID}
	if !ownOnly {
		where = `(runs.runner = ? OR runs.runner = '' OR o.last_seen IS NULL OR o.last_seen < ?)`
		args = append(args, now.Add(-e.runnerTimeout).UnixMilli())
	}
	if e.knows(schemaReruns) {
		// Runs that need a newer server are left to one.
		where += ` AND runs.min_schema <= ?`
		args = append(args, e.schema)
	}
	if e.knows(schemaDeploy) {
		// Deploy and rollback steps go to deploy runners (LeaseDeploy).
		where += ` AND s.target = ''`
	}
	rows, err := e.db.Query(`SELECT s.run_id, s.step_id, s.attempt FROM steps s
		JOIN runs ON runs.id = s.run_id
		LEFT JOIN runners o ON o.id = runs.runner
		WHERE s.status = 'queued' AND `+where+`
		ORDER BY s.queued_at, s.run_id, s.position LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	type cand struct {
		run, step string
		attempt   int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.run, &c.step, &c.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, cd := range cands {
		var job *Job
		err := e.withRun(cd.run, func(c *cmd) error {
			job = nil // the closure may run more than once
			s := c.st.Steps[cd.step]
			if s == nil || s.Status != StepQueuedS || s.Attempt != cd.attempt || c.st.CancelRequested {
				return nil // someone else got it, or it changed
			}
			if def := c.st.Pipeline.Step(cd.step); def == nil || def.IsDeployKind() {
				return nil // for deploy runners only
			}
			prev := c.st.Runner
			if !c.runnerCan(r, cd.step) {
				return nil // it needs a runner that knows artifacts
			}
			if prev != r.ID {
				if ownOnly {
					return nil
				}
				if prev != "" {
					alive, err := c.runnerAlive(prev)
					if err != nil || alive {
						return err // the run is held by another runner
					}
				}
			}
			leaseID := ids.New("lease")
			if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at) VALUES(?,?,?,?,?,?)`,
				leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil()); err != nil {
				return err
			}
			p := Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID}
			if c.e.knows(schemaReruns) {
				p.Inputs = c.inputs(cd.step)
			}
			c.emit(StepLeased, p)
			job = c.job(cd.step, leaseID)
			if err := c.addArtifactsToJob(job); err != nil {
				return err
			}
			if prev != "" && prev != r.ID {
				job.TakenOverFrom = prev
			}
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) {
			continue // a newer server will hand it out
		}
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, nil
}

// leaveNewRunsToIdle reports whether a runner should take steps only of the
// runs it holds: it holds a lease, and another runner that holds none has
// been seen within the lease TTL.
func (e *Engine) leaveNewRunsToIdle(runnerID string, now time.Time) (bool, error) {
	// Deploy runners (of a pool) never take ordinary steps, so they are not
	// idle runners to leave work to.
	notPool := ""
	if e.knows(schemaDeploy) {
		notPool = ` AND o.pool = ''`
	}
	var n int
	err := e.db.QueryRow(`SELECT COUNT(*) FROM runners o
		WHERE o.id <> ? AND o.last_seen >= ?`+notPool+`
		AND NOT EXISTS (SELECT 1 FROM leases WHERE leases.runner = o.id)
		AND EXISTS (SELECT 1 FROM leases WHERE leases.runner = ?)`,
		runnerID, now.Add(-e.ttl).UnixMilli(), runnerID).Scan(&n)
	return n > 0, err
}

// runnerAlive reports whether a runner has been seen within RunnerTimeout.
func (c *cmd) runnerAlive(id string) (bool, error) {
	var seen int64
	err := c.tx.QueryRow(`SELECT last_seen FROM runners WHERE id = ?`, id).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // it left, or was forgotten
	}
	return err == nil && seen >= c.now-c.e.runnerTimeout.Milliseconds(), err
}

// leaseRef is where a lease points.
type leaseRef struct {
	runID, stepID string
	attempt       int
}

// findLease looks a lease up. Any server can do this, whichever one granted
// the lease.
func (e *Engine) findLease(leaseID string) (leaseRef, error) {
	var l leaseRef
	err := e.db.QueryRow(`SELECT run_id, step_id, attempt FROM leases WHERE id = ?`, leaseID).Scan(&l.runID, &l.stepID, &l.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrLeaseLost
	}
	return l, err
}

// Start records that the runner began executing the step. It reports whether
// the run has been asked to cancel.
func (e *Engine) Start(leaseID string) (cancel bool, err error) {
	l, err := e.findLease(leaseID)
	if err != nil {
		return false, err
	}
	err = e.withRun(l.runID, func(c *cmd) error {
		cancel = false
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			return ErrLeaseLost
		}
		if err := c.renewLease(leaseID); err != nil {
			return err
		}
		if s.Status == StepLeasedS {
			c.emit(StepStarted, Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID})
		}
		cancel = c.st.CancelRequested
		return nil
	})
	return cancel, err
}

// Heartbeat renews a lease. It reports whether the run has been asked to
// cancel, in which case the runner should stop the step. A lease stays valid
// until it is reaped, so a heartbeat that arrives late but before the reaper
// still counts. It also records that the lease's runner was seen, so that a
// runner busy with long steps keeps hold of its runs.
func (e *Engine) Heartbeat(leaseID string) (cancel bool, err error) {
	unlock := e.writeLock()
	defer unlock()
	now := e.now()
	var runID, runnerID string
	err = e.db.QueryRow(`UPDATE leases SET expires_at = ? WHERE id = ? RETURNING run_id, runner`,
		now.Add(e.ttl).UnixMilli(), leaseID).Scan(&runID, &runnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, err
	}
	if err := sawRunner(e.db, runnerID, now); err != nil {
		return false, err
	}
	var flag int
	if err := e.db.QueryRow(`SELECT cancel_requested FROM runs WHERE id = ?`, runID).Scan(&flag); err != nil {
		return false, err
	}
	return flag != 0, nil
}

// Finish records how a step attempt ended.
//
// For a step that declares test reports, the files the runner collected
// are parsed first, outside the transaction; the attempt's summary goes in
// the event that ends it and the individual results in test_cases, in the
// same transaction. Nothing about the reports changes the step's outcome.
func (e *Engine) Finish(leaseID string, res Result) error {
	l, err := e.findLease(leaseID)
	if err != nil {
		return err
	}
	var tests *parsedTests
	if res.Tests != nil && e.knows(schemaTests) && (res.Outcome == "succeeded" || res.Outcome == "failed" || res.Outcome == "canceled") {
		tests = e.parseTests(res.Tests)
	}
	prep12 := e.prepare12(l, res)
	return e.withRun(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			return ErrLeaseLost
		}
		if err := c.endLease(leaseID); err != nil {
			return err
		}
		if def := c.st.Pipeline.Step(l.stepID); def != nil && def.IsDeployKind() {
			c.finishAction(l.stepID, s, def, leaseID, res)
			c.advance()
			return nil
		}
		p := Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID, ExitCode: res.ExitCode, Outputs: res.Outputs}
		if def := c.st.Pipeline.Step(l.stepID); def != nil && def.IsImage() && c.e.knows(schemaImages) {
			// An image step's outputs are what it made, never what anything
			// else says.
			p.Outputs = nil
			if res.Image.clean() {
				p.Image = res.Image
				if res.Outcome == "succeeded" {
					p.Outputs = imageOutputs(res.Image)
				}
			}
		}
		if def := c.st.Pipeline.Step(l.stepID); tests != nil && def != nil && def.Reports != nil {
			var err error
			if p.Tests, err = c.keepTests(l.stepID, l.attempt, tests); err != nil {
				return err
			}
		}
		if err := c.finish12(l, res, &p, prep12); err != nil {
			return err
		}
		switch res.Outcome {
		case "succeeded":
			p.Outcome = string(StepSucceeded)
			c.emit(StepFinished, p)
		case "lost":
			if c.st.CancelRequested {
				p.Reason = c.st.stoppedBecause()
				p.Tests = nil // the machinery failed; nothing it collected is kept
				p.Findings, p.SBOM, p.Artifacts, p.Image = nil, nil, nil, nil
				c.dropTests(l.stepID, l.attempt)
				c.emit(StepCanceled, p)
			} else {
				c.dropTests(l.stepID, l.attempt)
				c.lose(l.stepID, nonEmpty(res.Error, "the runner could not run the step"))
			}
		case "canceled":
			if c.st.CancelRequested {
				p.Reason = c.st.stoppedBecause()
				c.emit(StepCanceled, p)
			} else {
				// The runner gave up without being asked, for example
				// because it is shutting down. Treat it like a lost runner.
				c.dropTests(l.stepID, l.attempt)
				c.lose(l.stepID, nonEmpty(res.Error, "the runner stopped the step"))
			}
		default:
			p.Outcome = string(StepFailed)
			p.Reason = nonEmpty(res.Error, "the step failed")
			p.Excerpt = CleanExcerpt(res.Excerpt)
			c.emit(StepFinished, p)
			def := c.st.Pipeline.Step(l.stepID)
			if def != nil && s.Failures <= def.Retry && !c.st.CancelRequested {
				c.emit(StepQueued, Payload{Step: l.stepID, Attempt: s.Attempt + 1, Reason: "retry"})
			}
		}
		c.advance()
		return nil
	})
}

// Release hands back a step that was leased but never started, for example
// because the runner disconnected before receiving it. It is queued again
// at once, as the same attempt.
func (e *Engine) Release(leaseID string) error {
	l, err := e.findLease(leaseID)
	if err != nil {
		return err
	}
	return e.withRun(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || s.Status != StepLeasedS {
			return ErrLeaseLost
		}
		if err := c.endLease(leaseID); err != nil {
			return err
		}
		if c.st.CancelRequested {
			c.emit(StepCanceled, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: c.st.canceledBecause()})
		} else {
			c.emit(StepQueued, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: "handed back before it started"})
		}
		c.advance()
		return nil
	})
}

// Limits on log volume, to protect the database from runaway output.
const (
	MaxLogLineBytes = 64 * 1024
	MaxLogBatch     = 2000
)

// AppendLogs stores output lines for the step held by leaseID. Sending logs
// also renews the lease, and records that its runner was seen.
func (e *Engine) AppendLogs(leaseID string, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > MaxLogBatch {
		return fmt.Errorf("%w: at most %d lines per call", ErrConflict, MaxLogBatch)
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Taking the sequence numbers locks the lease row, so concurrent calls
	// for the same lease (on any server) get separate ranges.
	var l leaseRef
	var last int
	var runnerID string
	now := e.now()
	err = tx.QueryRow(`UPDATE leases SET log_seq = log_seq + ?, expires_at = ? WHERE id = ?
		RETURNING run_id, step_id, attempt, log_seq, runner`, len(lines), now.Add(e.ttl).UnixMilli(), leaseID).
		Scan(&l.runID, &l.stepID, &l.attempt, &last, &runnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if err := sawRunner(tx, runnerID, now); err != nil {
		return err
	}
	first := last - len(lines) + 1
	out := make([]LogLine, len(lines))
	for i, ln := range lines {
		if len(ln.Text) > MaxLogLineBytes {
			ln.Text = ln.Text[:MaxLogLineBytes] + " …[line truncated]"
		}
		if ln.At == 0 {
			ln.At = e.now().UnixMilli()
		}
		if ln.Stream == "" {
			ln.Stream = "stdout"
		}
		ln.Seq = first + i
		out[i] = ln
		if _, err := tx.Exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?,?,?,?,?,?,?)`,
			l.runID, l.stepID, l.attempt, ln.Seq, ln.At, ln.Stream, ln.Text); err != nil {
			return err
		}
	}
	if err := e.notifyOthers(tx, note{Kind: noteLog, Run: l.runID, Step: l.stepID, Attempt: l.attempt, FromSeq: first, ToSeq: last}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.hub.Publish(hub.Message{Type: "log", RunID: l.runID, Data: LogBatch{Step: l.stepID, Attempt: l.attempt, Lines: out}})
	return nil
}

// LogBatch is the live update sent when log lines arrive.
type LogBatch struct {
	Step    string    `json:"step"`
	Attempt int       `json:"attempt"`
	Lines   []LogLine `json:"lines"`
}

// ---- lease expiry ----

// reaper runs on every server. Each tick it records that a server is alive,
// expires leases whose runners have gone quiet, and rejects approvals that
// nobody decided in time. Every minute it also forgets runners long gone.
func (e *Engine) reaper(ctx context.Context) {
	defer e.bg.Done()
	interval := min(max(e.ttl/10, 50*time.Millisecond), time.Second)
	t := time.NewTicker(interval)
	defer t.Stop()
	var pruned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.deployKick:
			e.dispatchLocks()
			continue
		case <-t.C:
			e.markAlive()
			e.reap()
			e.expireWaits()
			e.dispatchLocks()
			if now := e.now(); now.Sub(pruned) >= time.Minute {
				pruned = now
				e.forgetRunners()
				e.pruneTests()
				e.pruneFindings()
				e.pruneArtifacts()
				e.PruneTriggers(now.Add(-7 * 24 * time.Hour).UnixMilli())
			}
		}
	}
}

func (e *Engine) markAlive() {
	unlock := e.writeLock()
	defer unlock()
	if err := setAlive(e.db, e.now()); err != nil {
		e.log.Error("recording that this server is alive", "err", err)
	}
}

// reap expires leases that have run out. Several servers may reap at once;
// each lease is ended by exactly one of them, because the row is deleted in
// the same transaction that records the step as lost.
func (e *Engine) reap() {
	q, args := `SELECT id, run_id, step_id, runner FROM leases WHERE expires_at < ?`, []any{e.now().UnixMilli()}
	if e.knows(schemaReruns) {
		// Runs that need a newer server are left to one.
		q = `SELECT l.id, l.run_id, l.step_id, l.runner FROM leases l LEFT JOIN runs r ON r.id = l.run_id
			WHERE l.expires_at < ? AND (r.min_schema IS NULL OR r.min_schema <= ?)`
		args = append(args, e.schema)
	}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		e.log.Error("looking for expired leases", "err", err)
		return
	}
	type expired struct{ id, runID, stepID, runner string }
	var found []expired
	for rows.Next() {
		var l expired
		if err := rows.Scan(&l.id, &l.runID, &l.stepID, &l.runner); err != nil {
			rows.Close()
			e.log.Error("looking for expired leases", "err", err)
			return
		}
		found = append(found, l)
	}
	rows.Close()
	for _, l := range found {
		err := e.withRun(l.runID, func(c *cmd) error {
			// Since the query above, another server may have reaped the
			// lease or the runner may have renewed it.
			res, err := c.tx.Exec(`DELETE FROM leases WHERE id = ? AND expires_at < ?`, l.id, c.now)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				return err
			}
			s := c.st.Steps[l.stepID]
			if s != nil && s.LeaseID == l.id && s.Status.Active() {
				c.lose(l.stepID, "the runner stopped responding")
				c.advance()
				c.after(func() { e.log.Warn("runner stopped responding", "run", l.runID, "step", l.stepID, "runner", l.runner) })
			}
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) {
			continue // left to a newer server
		}
		if errors.Is(err, ErrNotFound) {
			// A lease for a run that does not exist: nothing to recover.
			unlock := e.writeLock()
			_, err = e.db.Exec(`DELETE FROM leases WHERE id = ?`, l.id)
			unlock()
		}
		if err != nil {
			e.log.Error("expiring a lease", "lease", l.id, "err", err)
		}
	}
}

// ---- the transaction helper ----

type cmd struct {
	e       *Engine
	tx      *txx
	runID   string
	st      *RunState
	runSeq  int64 // run_seq of the last event already stored
	now     int64
	events  []Event
	changed map[string]bool
	queued  bool
	hooks   []func()
	// others are the other runs this command changed (see alsoRun).
	others []*cmd
	// tests are the test results to store with this command's events, by
	// step attempt (see keepTests).
	tests map[stepAttempt][]storedCase
	// schema12 is what schema 12 stores with the command's events.
	schema12 *schema12Data
	// schemaBefore is the run's Schema before this command, so that
	// runs.min_schema is written only when it changes.
	schemaBefore int
	// err is a database error met while moving the run forward (see
	// dbFail); the command fails with it.
	err error
}

func (c *cmd) emit(t EventType, p Payload) {
	if c.st == nil {
		c.st = &RunState{vocab: c.e.schema}
	}
	ev := Event{RunID: c.runID, Type: t, At: c.now, Data: p}
	c.st.apply(ev)
	c.events = append(c.events, ev)
	if t == RunCreated {
		for _, id := range c.st.Order {
			c.changed[id] = true
		}
	}
	if p.Step != "" {
		c.changed[p.Step] = true
	}
	if t == StepQueued {
		c.queued = true
	}
}

// after runs fn once the transaction has committed. It never runs for an
// attempt that was rolled back.
func (c *cmd) after(fn func()) { c.hooks = append(c.hooks, fn) }

// leaseUntil is when a lease granted or renewed now runs out.
func (c *cmd) leaseUntil() int64 { return c.now + c.e.ttl.Milliseconds() }

// renewLease extends a lease inside the transaction.
func (c *cmd) renewLease(leaseID string) error {
	res, err := c.tx.Exec(`UPDATE leases SET expires_at = ? WHERE id = ?`, c.leaseUntil(), leaseID)
	return leaseChanged(res, err)
}

// endLease deletes a lease inside the transaction. Exactly one transaction
// can do this for a given lease; the others get ErrLeaseLost.
func (c *cmd) endLease(leaseID string) error {
	res, err := c.tx.Exec(`DELETE FROM leases WHERE id = ?`, leaseID)
	return leaseChanged(res, err)
}

func leaseChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// maxAttempts bounds how often a command is retried because another
// transaction changed the same run first.
const maxAttempts = 10

// withRun runs fn against the run's current state and stores what it emits.
//
// fn may run several times: if another transaction (on this server or
// another) changes the run first, everything is rolled back and fn runs
// again on the new state. So fn must not have effects outside the
// transaction, other than through c.after.
func (e *Engine) withRun(runID string, fn func(c *cmd) error) error {
	return e.runTx(runID, false, fn)
}

// runTx is withRun that, if create is set, also accepts a run that does not
// exist yet.
func (e *Engine) runTx(runID string, create bool, fn func(c *cmd) error) error {
	for attempt := 1; ; attempt++ {
		c, err := e.attemptTx(runID, create, fn)
		if err == nil {
			for _, x := range append([]*cmd{c}, c.others...) {
				for _, h := range x.hooks {
					h()
				}
				if len(x.events) > 0 {
					e.publishChanges(x.runID, x.changed)
					if x.queued {
						e.notify()
					}
				}
			}
			return nil
		}
		if !isConflict(err) {
			return err
		}
		if attempt == maxAttempts {
			return fmt.Errorf("run %s kept changing underneath this command (%d attempts): %w", runID, attempt, err)
		}
		// Back off a little, at random, so competing servers spread out.
		time.Sleep(time.Duration(rand.Int64N(int64(attempt)*int64(2*time.Millisecond))) + time.Millisecond)
	}
}

// attemptTx is one try of runTx: one transaction, committed only if fn and
// storing its events succeed.
func (e *Engine) attemptTx(runID string, create bool, fn func(c *cmd) error) (*cmd, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	evs, runSeq, err := loadEvents(tx, runID)
	if err != nil {
		return nil, err
	}
	c := &cmd{e: e, tx: tx, runID: runID, st: e.fold(evs), runSeq: runSeq, now: e.now().UnixMilli(), changed: map[string]bool{}}
	if c.st == nil {
		if !create {
			return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
		}
		c.st = &RunState{vocab: e.schema}
	}
	c.schemaBefore = c.st.Schema
	if err := needsNewer(c.st); err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if c.err != nil {
		return nil, c.err
	}
	if len(c.events) > 0 {
		if err := c.persist(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

// fold rebuilds a run's state as this server understands it.
func (e *Engine) fold(evs []Event) *RunState { return fold(evs, e.schema) }

// needsNewer returns ErrNeedsNewer, saying why, for a run this server does
// not fully understand.
func needsNewer(st *RunState) error {
	if len(st.NotUnderstood) == 0 {
		return nil
	}
	return fmt.Errorf("%w: run %s has %s, written by a newer version of Launchway; this server leaves it alone, "+
		"and a server of that version must handle it", ErrNeedsNewer, st.ID, strings.Join(st.NotUnderstood, ", "))
}

// loadEvents returns a run's events in order, and the run_seq of the last.
func loadEvents(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, runID string) ([]Event, int64, error) {
	rows, err := q.Query(`SELECT seq, run_seq, type, at, data FROM events WHERE run_id = ? ORDER BY run_seq`, runID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var evs []Event
	var last int64
	for rows.Next() {
		var err error
		ev := Event{RunID: runID}
		var data string
		if err := rows.Scan(&ev.Seq, &last, &ev.Type, &ev.At, &data); err != nil {
			return nil, 0, err
		}
		if ev.Data, ev.problem, err = decodePayload(ev.Type, []byte(data)); err != nil {
			return nil, 0, fmt.Errorf("event %d: %w", ev.Seq, err)
		}
		evs = append(evs, ev)
	}
	return evs, last, rows.Err()
}

// decodePayload reads an event's details. Fields it does not know are
// ignored: a newer server may add fields that only record more (and sets
// Payload.Requires when older servers must not ignore one). The pipeline of
// a run.created event is read strictly instead, because a key this build
// does not know could change what a step does: problem then says so, and
// the run is left to a newer server.
func decodePayload(t EventType, data []byte) (p Payload, problem string, err error) {
	if t != RunCreated {
		err = json.Unmarshal(data, &p)
		return p, "", err
	}
	var w struct {
		Payload
		Pipeline json.RawMessage `json:"pipeline,omitempty"` // shadows Payload.Pipeline
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return p, "", err
	}
	p = w.Payload
	if len(w.Pipeline) == 0 || string(w.Pipeline) == "null" {
		return p, "", nil
	}
	p.Pipeline = new(pipeline.Pipeline)
	dec := json.NewDecoder(bytes.NewReader(w.Pipeline))
	dec.DisallowUnknownFields()
	if strictErr := dec.Decode(p.Pipeline); strictErr != nil {
		p.Pipeline = new(pipeline.Pipeline)
		if err := json.Unmarshal(w.Pipeline, p.Pipeline); err != nil {
			return p, "", err
		}
		return p, "the pipeline: " + strings.TrimPrefix(strictErr.Error(), "json: "), nil
	}
	return p, "", nil
}

// persist stores the new events and brings the derived tables up to date.
// The events go first: if another transaction has stored the same run_seq,
// this fails with a conflict before anything else is touched.
func (c *cmd) persist() error {
	for i := range c.events {
		ev := &c.events[i]
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		c.runSeq++
		if err := c.tx.QueryRow(`INSERT INTO events(run_id, run_seq, type, at, data) VALUES(?,?,?,?,?) RETURNING seq`,
			ev.RunID, c.runSeq, ev.Type, ev.At, string(data)).Scan(&ev.Seq); err != nil {
			return err
		}
		c.st.LastSeq = ev.Seq
	}
	st := c.st
	t := st.Trigger
	draft, hash := 0, ""
	if st.Draft != nil {
		draft, hash = 1, st.Draft.Hash
	}
	if _, err := c.tx.Exec(`INSERT INTO runs(id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
			actor, message, author, created_at, started_at, finished_at, last_seq, runner, draft, pipeline_hash)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, cancel_requested = excluded.cancel_requested,
			started_at = excluded.started_at, finished_at = excluded.finished_at, last_seq = excluded.last_seq,
			runner = excluded.runner`,
		st.ID, st.Project, st.Number, st.Pipeline.Name, st.Status, boolInt(st.CancelRequested), t.Event, t.Ref, t.Branch, t.Tag, t.SHA,
		t.Actor, t.Message, t.Author, st.CreatedAt, st.StartedAt, st.FinishedAt, st.LastSeq, st.Runner, draft, hash); err != nil {
		return err
	}
	changed := c.changedSteps()
	for _, id := range changed {
		s := st.Steps[id]
		outputs, _ := json.Marshal(s.Outputs)
		if s.Outputs == nil {
			outputs = []byte("{}")
		}
		if _, err := c.tx.Exec(`INSERT INTO steps(run_id, step_id, position, status, attempt, queued_at, started_at, finished_at,
				exit_code, error, skip_kind, runner, lease_id, outputs, deadline, decided_by, decided_at, comment, excerpt)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run_id, step_id) DO UPDATE SET status = excluded.status, attempt = excluded.attempt,
				queued_at = excluded.queued_at, started_at = excluded.started_at, finished_at = excluded.finished_at,
				exit_code = excluded.exit_code, error = excluded.error, skip_kind = excluded.skip_kind,
				runner = excluded.runner, lease_id = excluded.lease_id, outputs = excluded.outputs,
				deadline = excluded.deadline, decided_by = excluded.decided_by, decided_at = excluded.decided_at,
				comment = excluded.comment, excerpt = excluded.excerpt`,
			st.ID, id, s.Position, s.Status, s.Attempt, s.QueuedAt, s.StartedAt, s.FinishedAt,
			s.ExitCode, s.Error, s.SkipKind, s.Runner, s.LeaseID, string(outputs),
			s.Deadline, s.DecidedBy, s.DecidedAt, s.Comment, s.Excerpt); err != nil {
			return err
		}
	}
	if c.e.knows(schemaReruns) {
		if err := c.persistSchema8(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaPullRequests) {
		if err := c.persistSchema9(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaTests) {
		if err := c.persistSchema10(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaDeploy) {
		if err := c.persistSchema11(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaArtifacts) {
		if err := c.persistSchema12(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaControl) {
		if err := c.persistControl(); err != nil {
			return err
		}
	}
	if c.e.knows(schemaImages) {
		if err := c.persistImages(); err != nil {
			return err
		}
	}
	return c.e.notifyOthers(c.tx, note{Kind: noteRun, Run: c.runID, Steps: changed, Queued: c.queued})
}

// persistSchema8 writes what schema 8 added to the derived tables: the
// schema the run needs, re-run links, reused steps and step inputs. It is
// separate so that a build limited to older schemas (schemaLimit) writes
// exactly what an older release would.
func (c *cmd) persistSchema8() error {
	st := c.st
	created := false
	for _, ev := range c.events {
		created = created || ev.Type == RunCreated
	}
	if created || st.Schema != c.schemaBefore {
		var of, from string
		var ofNumber int
		if st.Rerun != nil {
			of, ofNumber, from = st.Rerun.Of, st.Rerun.OfNumber, st.Rerun.From
		}
		if _, err := c.tx.Exec(`UPDATE runs SET min_schema = ?, rerun_of = ?, rerun_of_number = ?, rerun_from = ? WHERE id = ?`,
			st.Schema, of, ofNumber, from, st.ID); err != nil {
			return err
		}
	}
	for _, ev := range c.events {
		switch {
		case ev.Type == StepReused:
			s := st.Steps[ev.Data.Step]
			if _, err := c.tx.Exec(`UPDATE steps SET reused_from = ?, reused_from_number = ?, reused_attempt = ? WHERE run_id = ? AND step_id = ?`,
				s.ReusedFrom, s.ReusedNumber, s.ReusedAttempt, st.ID, s.ID); err != nil {
				return err
			}
		case ev.Type == StepLeased && ev.Data.Inputs != nil:
			if err := storeInputs(c.tx, st.ID, ev.Data.Step, ev.Data.Attempt, ev.Data.Inputs); err != nil {
				return err
			}
		}
	}
	return nil
}

// persistSchema9 writes why the run was asked to cancel, when that changes.
func (c *cmd) persistSchema9() error {
	for _, ev := range c.events {
		switch ev.Type {
		case RunCreated, RunCancelRequested, RunResumed:
			st := c.st
			by, byNumber := "", 0
			if st.SupersededBy != nil {
				by, byNumber = st.SupersededBy.Run, st.SupersededBy.Number
			}
			_, err := c.tx.Exec(`UPDATE runs SET cancel_reason = ?, superseded_by = ?, superseded_by_number = ? WHERE id = ?`,
				st.CancelReason, by, byNumber, st.ID)
			return err
		}
	}
	return nil
}

// changedSteps returns the IDs of the steps this command changed, sorted so
// that rows are always written in the same order.
func (c *cmd) changedSteps() []string {
	ids := make([]string, 0, len(c.changed))
	for id := range c.changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// advance moves the run forward: queue steps whose needs are met (or, for
// approvals, start waiting for a decision), skip steps that cannot run,
// cancel what remains if a cancel was asked for, and finish the run when
// nothing is left to do. A waiting approval is not finished, so neither is
// its run.
func (c *cmd) advance() {
	st := c.st
	if st.Status.Terminal() {
		return
	}
	ctx := st.Trigger.Context()
	for changed := true; changed; {
		changed = c.failFast()
		for _, id := range st.Order {
			s := st.Steps[id]
			if st.CancelRequested && (s.Status == StepPending || s.Status == StepQueuedS || s.Status == StepWaitingS) {
				c.emit(StepCanceled, Payload{Step: id, Reason: st.canceledBecause()})
				changed = true
				continue
			}
			if s.Status != StepPending {
				continue
			}
			def := st.Pipeline.Step(id)
			if def.IsRollback() {
				// The rollback rule replaces the usual one: it runs because
				// a step it needs did not succeed.
				if c.advanceRollback(id, def) {
					changed = true
				}
				continue
			}
			ready, blocked := true, ""
			for _, n := range def.Needs {
				switch dep := st.Steps[n]; dep.Status {
				case StepSucceeded:
				case StepFailed, StepSkippedS, StepCanceledS:
					if blocked == "" {
						blocked = fmt.Sprintf("%s %s", n, dep.Status)
					}
				default:
					ready = false
				}
			}
			condSkip := ""
			if rule := def.When.UpstreamRule(); rule != pipeline.UpstreamSucceeded {
				ready, blocked, condSkip = c.upstreamGate(def, rule)
			}
			switch {
			case blocked != "":
				c.emit(StepSkipped, Payload{Step: id, SkipKind: "upstream", Reason: "not run because " + blocked})
				changed = true
			case !ready:
			case condSkip != "":
				c.emit(StepSkipped, Payload{Step: id, SkipKind: "condition", Reason: condSkip})
				changed = true
			case !def.When.Match(ctx):
				c.emit(StepSkipped, Payload{Step: id, SkipKind: "condition", Reason: "its when condition did not match"})
				changed = true
			case c.loopHolds(id):
				// Its loop has max_parallel members going; it waits its turn.
			case def.IsDeploy():
				before := len(c.events)
				c.startDeploy(id, def)
				if c.err != nil {
					return
				}
				changed = len(c.events) > before
			case def.IsRolloutStep():
				c.startRolloutStep(id, def)
				changed = true
			case def.IsApproval():
				c.emit(StepWaiting, Payload{Step: id, Attempt: s.Attempt + 1, Deadline: c.now + def.Timeout.D().Milliseconds()})
				changed = true
			case def.IsImage() && c.imageRefused(id, def):
				changed = true
			case !st.secretsAllowed() && len(def.Secrets) > 0:
				// Never handed to a runner, so no runner looks a secret up
				// for text that nobody reviewed.
				reason := draftSecretsRefused(st.Project, def)
				if st.Draft == nil {
					reason = pullRequestSecretsRefused(st.Project, def)
				}
				c.emit(StepFinished, Payload{Step: id, Outcome: string(StepFailed), Reason: reason})
				changed = true
			default:
				c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1})
				changed = true
			}
		}
	}
	c.closeDeployments()
	failed, canceled := false, false
	for _, id := range st.Order {
		switch st.Steps[id].Status {
		case StepSucceeded, StepSkippedS:
		case StepFailed:
			failed = true
		case StepCanceledS:
			canceled = true
		default:
			return // something is still pending or running
		}
	}
	status := RunSucceeded
	switch {
	case failed:
		status = RunFailed
	case canceled:
		status = RunCanceled
	}
	c.emit(RunFinished, Payload{Status: string(status)})
}

// lose records that a step's runner disappeared, and queues it again unless
// it has been lost too often or the run is being canceled.
func (c *cmd) lose(stepID, reason string) {
	s := c.st.Steps[stepID]
	c.emit(StepLost, Payload{Step: stepID, Attempt: s.Attempt, Reason: reason})
	switch {
	case c.st.CancelRequested:
		c.emit(StepCanceled, Payload{Step: stepID, Attempt: s.Attempt, Reason: c.st.canceledBecause()})
	case s.Lost <= c.e.maxLost:
		c.emit(StepQueued, Payload{Step: stepID, Attempt: s.Attempt + 1, Reason: "requeued after: " + reason})
	default:
		c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed),
			Reason: fmt.Sprintf("%s (gave up after %d tries)", reason, s.Lost)})
	}
}

// job builds what the runner needs, including the outputs of needed steps.
func (c *cmd) job(stepID, leaseID string) *Job {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	env := map[string]string{}
	for _, v := range c.stepEnv(stepID) {
		env[v.Name] = v.Value
	}
	return &Job{
		Container: st.Pipeline.ContainerFor(def), Containers: st.Pipeline.RunContainers(),
		LeaseID: leaseID, LeaseTTL: pipeline.Duration(c.e.ttl),
		RunID: st.ID, Project: st.Project, RunNumber: st.Number, Pipeline: st.Pipeline.Name,
		// advance fails a draft's step that asks for secrets it may not
		// read before it is queued; stepSecrets is a second guard, so that
		// a runner is never asked to look a secret up for one.
		StepID: stepID, Attempt: s.Attempt, Image: def.Image, Script: def.Run, Env: env, Secrets: c.stepSecrets(def),
		Timeout: def.Timeout, Source: st.Source, SHA: st.Trigger.SHA, Workspace: st.Workspace, Library: def.Library,
		Reports: c.jobReports(def), ImageBuild: c.imageJobFor(def),
	}
}

// imageJobFor is an image step's job, or nil for other steps. advance
// checked that its values can be worked out before queueing it.
func (c *cmd) imageJobFor(def *pipeline.Step) *ImageJob {
	if !def.IsImage() || !c.e.knows(schemaImages) {
		return nil
	}
	j, err := c.imageJob(def)
	if err != nil {
		return nil
	}
	return j
}

// localID is how a step refers to another: by the ID it has within the
// template the step is in, as the template's author wrote it, or by its full
// ID if it is not in that template.
func localID(def *pipeline.Step, id string) string {
	for g := def.Group; g != ""; {
		if rest, ok := strings.CutPrefix(id, g+"/"); ok {
			return rest
		}
		i := strings.LastIndex(g, "/")
		if i < 0 {
			break
		}
		g = g[:i]
	}
	return id
}

// inGroups reports whether a step is one of the groups' steps.
func inGroups(id string, groups []string) bool {
	for _, g := range groups {
		if strings.HasPrefix(id, g+"/") {
			return true
		}
	}
	return false
}

// ---- live updates and wake-ups ----

// publishChanges sends the current state of a run and of the given steps to
// this server's hub, reading them back from the database.
func (e *Engine) publishChanges(runID string, steps map[string]bool) {
	if sum, err := e.runSummary(runID); err == nil {
		e.hub.Publish(hub.Message{Type: "run", RunID: runID, Data: sum})
	}
	for id := range steps {
		if v, err := e.stepView(runID, id); err == nil {
			e.hub.Publish(hub.Message{Type: "step", RunID: runID, Data: v})
		}
	}
}

func (e *Engine) waitChan() <-chan struct{} {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	return e.wake
}

// notify wakes every Lease waiting on this server.
func (e *Engine) notify() {
	e.wakeMu.Lock()
	close(e.wake)
	e.wake = make(chan struct{})
	e.wakeMu.Unlock()
}

// Runner rows are kept for the runners list and for run affinity (see Lease).
const (
	// RunnerOffline is how long a runner may go unseen before it is listed
	// as offline.
	RunnerOffline = 2 * time.Minute
	// forgetRunnerAfter is how long a runner may go unseen before its row is
	// removed. Several servers may remove the same rows; that is harmless.
	forgetRunnerAfter = 24 * time.Hour
	// seenEvery bounds how often heartbeats and log uploads write that their
	// runner was seen: log batches arrive several times a second.
	seenEvery = 5 * time.Second
)

// sawRunner records that a runner was seen at now, unless that was recorded
// less than seenEvery ago. A runner that has no row is not added: rows are
// made by Lease, which knows the runner's name.
func sawRunner(q interface {
	Exec(string, ...any) (sql.Result, error)
}, id string, now time.Time) error {
	_, err := q.Exec(`UPDATE runners SET last_seen = ? WHERE id = ? AND last_seen < ?`,
		now.UnixMilli(), id, now.Add(-seenEvery).UnixMilli())
	return err
}

// RunnerLeft forgets a runner that is shutting down and holds no more steps,
// so that other runners may take over its runs at once instead of after
// RunnerTimeout.
func (e *Engine) RunnerLeft(id string) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM runners WHERE id = ?`, id); err != nil {
		return err
	}
	// Runners waiting on other servers may now take over its runs.
	if err := e.notifyOthers(tx, note{Kind: noteWake}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.notify()
	return nil
}

// forgetRunners removes runners not seen for forgetRunnerAfter.
func (e *Engine) forgetRunners() {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`DELETE FROM runners WHERE last_seen < ?`, e.now().Add(-forgetRunnerAfter).UnixMilli())
	if err != nil {
		e.log.Error("forgetting runners that are long gone", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		e.log.Info("forgot runners not seen for a day", "count", n)
	}
}

func (e *Engine) touchRunner(r RunnerInfo) {
	if r.ID == "" {
		return
	}
	unlock := e.writeLock()
	defer unlock()
	if e.knows(schemaImages) {
		if _, err := e.db.Exec(`INSERT INTO runners(id, name, executor, version, last_seen, targets, pool, features) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, executor = excluded.executor, version = excluded.version,
				last_seen = excluded.last_seen, targets = excluded.targets, pool = excluded.pool, features = excluded.features`,
			r.ID, r.Name, r.Executor, r.Version, e.now().UnixMilli(), strings.Join(r.Targets, ","), r.Pool, r.Features); err != nil {
			e.log.Error("recording runner", "err", err)
		}
		return
	}
	if e.knows(schemaDeploy) {
		if _, err := e.db.Exec(`INSERT INTO runners(id, name, executor, version, last_seen, targets, pool) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, executor = excluded.executor,
				version = excluded.version, last_seen = excluded.last_seen, targets = excluded.targets, pool = excluded.pool`,
			r.ID, r.Name, r.Executor, r.Version, e.now().UnixMilli(), strings.Join(r.Targets, ","), r.Pool); err != nil {
			e.log.Error("recording runner", "err", err)
		}
		return
	}
	if _, err := e.db.Exec(`INSERT INTO runners(id, name, executor, version, last_seen) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, executor = excluded.executor,
			version = excluded.version, last_seen = excluded.last_seen`,
		r.ID, r.Name, r.Executor, r.Version, e.now().UnixMilli()); err != nil {
		e.log.Error("recording runner", "err", err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
