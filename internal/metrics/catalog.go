package metrics

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Default is the registry every part of the product records into.
var Default = NewRegistry()

var (
	durationBuckets = []float64{0.005, 0.025, 0.1, 0.5, 1, 2.5, 10, 30, 120, 600, 1800, 7200}
	httpBuckets     = []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 10, 30, 120}
	queryBuckets    = []float64{0.0005, 0.002, 0.01, 0.05, 0.25, 1, 5, 30}
	waitBuckets     = []float64{1, 5, 15, 60, 300, 900, 3600, 14400}
)

// Pool, route, outcome and similar label values come from small closed sets;
// each metric's help says which.
var (
	HTTPRequests = Default.New(Desc{Name: "launchway_http_requests_total", Kind: Counter, Labels: []string{"route", "method", "class"},
		Help: "HTTP requests answered, by route template (never the path), method and status class (2xx, 3xx, 4xx, 5xx)."})
	HTTPDuration = Default.New(Desc{Name: "launchway_http_request_duration_seconds", Kind: Histogram, Labels: []string{"route", "method"}, Buckets: httpBuckets,
		Help: "Time to answer an HTTP request, by route template and method."})
	HTTPInFlight = Default.New(Desc{Name: "launchway_http_requests_in_flight", Kind: Gauge,
		Help: "HTTP requests being served now."})

	GitOps = Default.New(Desc{Name: "launchway_git_operations_total", Kind: Counter, Labels: []string{"operation", "outcome"},
		Help: "Git operations served or made: operation is fetch (served to a client), push (received) or checkout (a Station's clone); outcome is ok or error."})
	GitDuration = Default.New(Desc{Name: "launchway_git_operation_duration_seconds", Kind: Histogram, Labels: []string{"operation"}, Buckets: durationBuckets,
		Help: "Time a Git operation took, by operation."})
	GitBytes = Default.New(Desc{Name: "launchway_git_transferred_bytes_total", Kind: Counter, Labels: []string{"operation", "direction"},
		Help: "Bytes moved by Git operations; direction is in (received) or out (sent)."})

	QueueDepth = Default.New(Desc{Name: "launchway_queue_depth", Kind: Gauge, Labels: []string{"pool"},
		Help: "Steps waiting in the holding pattern for a Station, by Station pool (the pool's name, or default)."})
	QueueWait = Default.New(Desc{Name: "launchway_queue_wait_seconds", Kind: Histogram, Labels: []string{"pool"}, Buckets: waitBuckets,
		Help: "Time a step waited in the holding pattern until a Station took it, by pool."})
	QueueOldest = Default.New(Desc{Name: "launchway_queue_oldest_wait_seconds", Kind: Gauge, Labels: []string{"pool"},
		Help: "How long the longest-waiting step has waited in the holding pattern, by pool; 0 when none waits."})

	RunsStarted = Default.New(Desc{Name: "launchway_runs_started_total", Kind: Counter,
		Help: "Sorties started."})
	RunsFinished = Default.New(Desc{Name: "launchway_runs_finished_total", Kind: Counter, Labels: []string{"outcome"},
		Help: "Sorties finished, by outcome (succeeded, failed, canceled)."})
	RunDuration = Default.New(Desc{Name: "launchway_run_duration_seconds", Kind: Histogram, Labels: []string{"outcome"}, Buckets: durationBuckets,
		Help: "Time from a Sortie starting to it finishing, by outcome."})
	StepsStarted = Default.New(Desc{Name: "launchway_steps_started_total", Kind: Counter,
		Help: "Steps (Sorties) started."})
	StepsFinished = Default.New(Desc{Name: "launchway_steps_finished_total", Kind: Counter, Labels: []string{"outcome"},
		Help: "Steps finished, by outcome (succeeded, failed, canceled, skipped)."})
	StepDuration = Default.New(Desc{Name: "launchway_step_duration_seconds", Kind: Histogram, Labels: []string{"outcome"}, Buckets: durationBuckets,
		Help: "Time a step ran, by outcome."})
	FlightRuns = Default.New(Desc{Name: "launchway_flight_runs_finished_total", Kind: Counter, Labels: []string{"flight", "outcome"}, Cap: 300,
		Help: "Sorties finished per Flight. Recorded only when observability.metrics.name_labels is on (default off), and held to 300 series."})

	StationsOnline = Default.New(Desc{Name: "launchway_stations_online", Kind: Gauge, Labels: []string{"pool"},
		Help: "Stations that reported in recently, by pool."})
	LeasesActive = Default.New(Desc{Name: "launchway_leases_active", Kind: Gauge, Labels: []string{"pool"},
		Help: "Step leases held by Stations now, by pool."})
	HeartbeatsMissed = Default.New(Desc{Name: "launchway_station_heartbeats_missed_total", Kind: Counter,
		Help: "Leases that expired because their Station stopped reporting."})
	StepRoomWaits = Default.New(Desc{Name: "launchway_step_room_waits_total", Kind: Counter, Labels: []string{"reason"},
		Help: "Times a Station could not start a step pod for lack of room and the step was put back in the holding pattern to try again; reason is quota_full or cluster_full."})

	CacheLookups = Default.New(Desc{Name: "launchway_cache_lookups_total", Kind: Counter, Labels: []string{"result"},
		Help: "Build cache lookups; result is hit or miss."})
	CacheBytes = Default.New(Desc{Name: "launchway_cache_bytes_total", Kind: Counter, Labels: []string{"direction"},
		Help: "Bytes of build cache moved; direction is restored or saved."})

	WebhookDeliveries = Default.New(Desc{Name: "launchway_webhook_deliveries_total", Kind: Counter, Labels: []string{"direction", "outcome"},
		Help: "Webhook deliveries; direction is in (received) or out (sent); outcome is accepted, rejected or failed."})
	MirrorSyncs = Default.New(Desc{Name: "launchway_mirror_syncs_total", Kind: Counter, Labels: []string{"outcome"},
		Help: "Mirror synchronizations, by outcome (ok or error)."})
	MirrorLag = Default.New(Desc{Name: "launchway_mirror_lag_seconds_max", Kind: Gauge,
		Help: "The longest time any mirror has gone without a successful synchronization; 0 when there are none."})

	Deploys = Default.New(Desc{Name: "launchway_deploys_total", Kind: Counter, Labels: []string{"verdict"},
		Help: "Launches closed, by verdict: `deployed`, `failed`, `rolled_back`, `rollback_failed` or `abandoned`."})
	Rollbacks = Default.New(Desc{Name: "launchway_rollbacks_total", Kind: Counter,
		Help: "Returns to base started, automatic or asked for."})
	ApprovalsWaiting = Default.New(Desc{Name: "launchway_approvals_waiting", Kind: Gauge,
		Help: "Approvals waiting for a decision now."})
	ApprovalOldest = Default.New(Desc{Name: "launchway_approval_oldest_age_seconds", Kind: Gauge,
		Help: "Age of the oldest approval still waiting; 0 when none waits."})

	DBConnections = Default.New(Desc{Name: "launchway_db_pool_connections", Kind: Gauge, Labels: []string{"state"},
		Help: "Database connections held by this process; state is open, in_use or idle."})
	DBWaits = Default.New(Desc{Name: "launchway_db_pool_wait_total", Kind: Counter,
		Help: "Times a query had to wait for a free database connection."})
	DBWaitSeconds = Default.New(Desc{Name: "launchway_db_pool_wait_seconds_total", Kind: Counter,
		Help: "Total time queries waited for a free database connection."})
	DBQueries = Default.New(Desc{Name: "launchway_db_query_duration_seconds", Kind: Histogram, Labels: []string{"family"}, Buckets: queryBuckets,
		Help: "Database statement durations by statement family: select, insert, update, delete, other. Never the statement text."})
	MigrationsApplied = Default.New(Desc{Name: "launchway_migrations_applied_total", Kind: Counter,
		Help: "Database migrations this process applied while starting."})
	SchemaVersion = Default.New(Desc{Name: "launchway_schema_version", Kind: Gauge,
		Help: "The database's schema version."})

	WorkerSuccess = Default.New(Desc{Name: "launchway_worker_last_success_timestamp_seconds", Kind: Gauge, Labels: []string{"worker"},
		Help: "When each background worker last finished a pass without error, as Unix seconds. A worker that stops shows a timestamp that stops moving."})
	SignIns = Default.New(Desc{Name: "launchway_sign_ins_total", Kind: Counter, Labels: []string{"method", "outcome"},
		Help: "Sign-in attempts; method is local or oidc; outcome is ok or denied."})
	TokenUses = Default.New(Desc{Name: "launchway_token_uses_total", Kind: Counter, Labels: []string{"kind"},
		Help: "Requests authenticated by a token, by kind (session, api, station, service)."})

	DataFreeBytes = Default.New(Desc{Name: "launchway_data_free_bytes", Kind: Gauge,
		Help: "Free bytes on the volume holding the server's data directory (where hosted repositories and the working files are kept); absent where the system does not say."})
	DataTotalBytes = Default.New(Desc{Name: "launchway_data_total_bytes", Kind: Gauge,
		Help: "Size in bytes of the volume holding the server's data directory."})

	MailMessages = Default.New(Desc{Name: "launchway_mail_messages_total", Kind: Counter, Labels: []string{"outcome"},
		Help: "Mail the outbox handled; outcome is sent, retried (a try failed, another is planned) or failed (given up). Never an address or a subject."})
	MailQueued = Default.New(Desc{Name: "launchway_mail_outbox_messages", Kind: Gauge, Labels: []string{"state"},
		Help: "Messages in the mail outbox by state: pending or failed."})

	RunnerPollAge = Default.New(Desc{Name: "launchway_station_last_poll_age_seconds", Kind: Gauge, Labels: []string{"loop"},
		Help: "Seconds since each loop of a Station or launch Station that takes work from the server last asked or got an answer; loop is a fixed name of the loop (lease for a Station; the launch Station has its own, such as observe and commands). A value that keeps growing is a stuck loop; /livez fails at five minutes."})
	RunnerActionsRunning = Default.New(Desc{Name: "launchway_station_actions_running", Kind: Gauge,
		Help: "Actions a Station or launch Station is carrying out now."})
	RunnerActionsAbandoned = Default.New(Desc{Name: "launchway_station_actions_abandoned_total", Kind: Counter,
		Help: "Actions a launch Station stopped waiting for after their time; the work may still be running somewhere."})

	Spans = Default.New(Desc{Name: "launchway_trace_spans_total", Kind: Counter, Labels: []string{"outcome"},
		Help: "Trace spans handed to the exporter; outcome is exported or dropped (its buffer was full or the endpoint failed)."})

	BuildInfo = Default.New(Desc{Name: "launchway_build_info", Kind: Gauge, Labels: []string{"version", "go_version"},
		Help: "Always 1; the version and Go version are its labels."})
	Uptime = Default.New(Desc{Name: "launchway_uptime_seconds", Kind: Gauge,
		Help: "Seconds since this process started."})
	Goroutines = Default.New(Desc{Name: "launchway_go_goroutines", Kind: Gauge,
		Help: "Goroutines in this process."})
	HeapBytes = Default.New(Desc{Name: "launchway_go_heap_alloc_bytes", Kind: Gauge,
		Help: "Bytes of heap in use in this process."})
)

var (
	startedAt time.Time
	startOnce sync.Once
)

var nameLabels atomic.Bool

// SetNameLabels turns the per-Flight metric on or off (observability.metrics.name_labels).
func SetNameLabels(on bool) { nameLabels.Store(on) }

// NameLabels says whether Flight names may be recorded as labels.
func NameLabels() bool { return nameLabels.Load() }

// Init records the build and starts the uptime and runtime gauges. It is
// safe to call more than once; the first call counts as the start.
func Init(version string) {
	startOnce.Do(func() {
		startedAt = time.Now()
		BuildInfo.Set(1, version, runtime.Version())
		Default.OnScrape(func() {
			Uptime.Set(time.Since(startedAt).Seconds())
			Goroutines.Set(float64(runtime.NumGoroutine()))
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			HeapBytes.Set(float64(m.HeapAlloc))
		})
	})
}

// Worker records that a background worker finished a pass without error.
func Worker(name string) { WorkerSuccess.Set(float64(time.Now().Unix()), name) }

// ClassOf returns the status class label for an HTTP status.
func ClassOf(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	}
	return "1xx"
}

// Family returns the statement family of SQL text: its first word, if it is
// one of the four common ones, and other for the rest.
func Family(q string) string {
	i := 0
	for i < len(q) && (q[i] == ' ' || q[i] == '\n' || q[i] == '\t' || q[i] == '\r') {
		i++
	}
	if len(q)-i >= 6 {
		switch w := q[i : i+6]; {
		case eqFold(w, "select"):
			return "select"
		case eqFold(w, "insert"):
			return "insert"
		case eqFold(w, "update"):
			return "update"
		case eqFold(w, "delete"):
			return "delete"
		}
	}
	return "other"
}

func eqFold(a, b string) bool {
	for i := 0; i < len(b); i++ {
		c := a[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != b[i] {
			return false
		}
	}
	return true
}
