# How Launchway is built

The packages of the code base and what the tests cover. For contributors.

| Package | What it does |
|---|---|
| `internal/pipeline` | Parses and validates the pipeline file into a typed graph |
| `internal/engine` | The durable run engine. Every change is an event in the database (PostgreSQL, or SQLite for one server) and state is rebuilt from events. Runners hold steps on leases; a lost runner's step is run again. Handles retries, cancel, resume and approvals. |
| `internal/executor` | Runs a step in a Podman or Docker container, in a Kubernetes pod, or on the host |
| `internal/runner` | Takes steps from the server over HTTP, streams logs, sends heartbeats |
| `internal/secrets` | Secret stores (env, file, Vault/OpenBao) and masking, used only by runners |
| `internal/source` | Mirrors Git repositories, starts runs on matching pushes and pull requests, records why |
| `internal/auth` | Roles and access bindings, OpenID Connect sign-in, and the cryptography sessions and tokens need |
| `internal/api` | Runner API, UI API, sign-in, API tokens, access checks, the Logbook and the live event stream |
| `internal/fakeidp` | An OpenID Connect provider for tests, which can be made to misbehave |
| `internal/blobstore` | Where artifacts are kept: the database (in chunks), a directory, or S3-compatible object storage |
| `internal/findings`, `internal/sbom` | Read SARIF scanner results and CycloneDX SBOMs, treating them as hostile input |
| `internal/policy` | Delivery policies: the rule catalog, policy files read with their lines, and the pure function that judges a run |
| `internal/imagebuild` | Image steps on the runner: BuildKit's client, pushing, scanning, signing and provenance |
| `internal/testreport`, `internal/coverage` | Read test result files in 13 formats and coverage files (Cobertura, JaCoCo, Clover, LCOV, Istanbul, Go), treating them as hostile input |
| `internal/verify`, `internal/verify/source` | Judge a deployment with plain statistics and log clustering, and read the metrics and logs from Prometheus, Loki, ClickHouse, Elasticsearch, PostgreSQL and JSON APIs |
| `internal/ai` | Optional calls to AI models (Anthropic, and OpenAI-compatible servers) for `ai` steps and explanations |
| `internal/runnerauth`, `internal/accept` | How an enrolled runner proves who it is, and a runner's own limits on the work it takes |
| `internal/api` (MCP files) | The `/mcp` endpoint for AI agents, built on the official Go SDK, calling the server's own API with the caller's token |
| `web/` | The React UI, compiled into the binary |

`make test` runs the Go tests with the race detector and type-checks the UI. The tests cover:

- crash recovery;
- lost and killed runners;
- many runners competing for the same steps;
- retries, cancel and resume;
- re-runs from a step: what is reused and never leased, outputs reaching the steps run again, refusals,
  approvals, drafts, library commits after a tag moved, two servers, and a real runner on the host;
- upgrades that can be rolled back: a build one schema older running against the newer database,
  runs it does not understand left alone, and servers migrating at the same moment;
- approvals, including two servers deciding or expiring the same one at once;
- pull requests from real repositories: closed ones not run, the default branch deciding, no
  secrets by default, and two servers polling the same pull requests;
- superseding: what is and is not superseded, all or nothing, and runs of one branch created at
  once on two servers while runners work on them;
- real Git repositories, including saves racing from two servers, stale saves and pushes the
  repository refuses;
- shared libraries: version resolution against real repositories, moving tags, overrides, a
  private library behind credentials, and a library script run at its pinned commit on the host,
  in containers and on Kubernetes;
- secrets kept out of the database, events, logs and API (`make test-vault` adds a real OpenBao);
- sign-in against a fake OpenID Connect provider: forged state, stolen codes (PKCE), bad ID tokens,
  open redirects, CSRF, session expiry, API tokens, a permission matrix over every route, the live
  stream, the Logbook, and sign-in across two servers;
- artifacts: chunked uploads that break off and carry on, checksum mismatches, quotas, pruning by
  two servers at once, fetching in a fresh workspace, and downloads that are never shown as pages
  (`make test-s3` adds a real S3-compatible server);
- security findings: real scanner output, hostile and fuzzed files, and secrets never stored;
- image steps with a real rootless BuildKit and registries with and without TLS and passwords: images
  pulled back by digest, the registry cache, build secrets in no layer, log, event or column, pull
  requests never pushing, readable failures, cosign 2 and 3 verifying signatures and attestations,
  two builders making one multi-platform index, and on Kubernetes building from a restricted step
  pod with the daemon deployed from `deploy/buildkit` (`make test-containers`, `make test-kubernetes`);
- policies: every rule pass and fail, modes, scope, waivers (expired, protected environments, one finding), decisions
  replayed from what they record, malformed and unknown rules refused with their lines, Git sources broken,
  unreachable and stale, two servers racing on one gate, a restart between a decision and the step, an older
  server leaving governed runs alone, and a deploy gate in front of real Argo CD (`make test-argo`);
- test reports and coverage: real output of the tools that write them, hostile and fuzzed files,
  attachments and hosted HTML reports in a sandbox;
- the built-in blob store: uploads that break off and carry on, damaged chunks refused, two servers
  on one part, copies between stores (`make test-postgres` runs them on PostgreSQL);
- verification: every statistic against known series, every kind of health source against a real or
  faked server, a deploy runner that dies mid-window, and an `ai` step against a fake model and a real
  small one (`make test-verify`, `make test-local-model`);
- runner identities and pools: enrolment, leases bound to a runner, pools by project and event,
  revocation and key rotation in the middle of a step, `accept:`, clock skew and proxies;
- the MCP endpoint: every tool, rights, limits, origins, OAuth and untrusted text;
- real containers (`make test-containers`);
- a real Kubernetes cluster (`make test-kubernetes`).
