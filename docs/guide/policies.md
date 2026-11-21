# Policies

Rules an organization sets for every pipeline, judged at run start, before deploying and before pushing an image. The full catalog is in the [policy rules reference](../reference/policy-rules.md).

Policies are rules an organization sets for every pipeline, which the pipeline's authors cannot
switch off: they live in the server's configuration, or in a Git repository the server reads at a
pinned tag, never in the pipeline file. Each policy is a name, a scope, a mode and rules taken from
a fixed catalog. There is no policy language: a rule is a typed setting with a fixed meaning,
checked when the configuration (or the policy file) is read, with its file and line.

```yaml
# launchway.yaml
policy:
  policies:
    - name: prod-evidence
      description: What reaches production shows passing tests, a clean scan and a second pair of eyes.
      mode: enforce                        # or report
      scope: { projects: [shop, "pay-*"], environments: [prod] }
      rules:
        tests: { min: 1 }                  # the steps before the deploy step reported tests, none failed
        findings: { max_severity: medium, scanners: [semgrep, gitleaks] }
        sbom: {}                           # a step before it made an SBOM
        deployed_images: { digest: true, built_by_run: true, signed: true, scanned: true }
        approvals: { min: 1 }              # by someone other than the person who started the run
        refs: { branches: [main], tags: ["v*"] }
    - name: pipeline-shape
      mode: report
      rules:
        library_versions: { pinned: tag }
        required_templates: [ { library: ci-templates, template: security-scan, versions: ["v3", "v3.*"] } ]
        step_images: { digest: true, registries: ["harbor.example.com/**"] }
        max_timeout: 2h
        forbid_secrets: { events: [pull_request, draft] }
        image_push: { scan: high, sign: runner, sbom: true, provenance: true, registries: [harbor] }
```

A run of `shop` on `main` that reaches `deploy-prod` without test results fails there, before
anything is committed or synced, and says why, on the graph, in the step and in the run's
**Policy** tab:

```
deploy-prod failed. blocked by policy: policy prod-evidence (enforce), rule tests: no step before
deploy-prod reported test results; needs at least 1 test result(s) from the steps before
deploy-prod, none failed. Grant a waiver or change what was found, then resume the run (or re-run
it from deploy-prod)
```

**Where policies are judged.** Each rule is judged at one point, in the same database transaction
as what the decision allows, so a decision and the step it lets through are stored together or not
at all, whatever server or restart comes between:

| Point | When | What a blocking decision does |
|---|---|---|
| run start | a run is created, re-run or resumed | each step of the run fails as it would start, saying why; nothing reaches a runner |
| before deploying | a deploy step is about to take its environment's lock; a rollout step is about to promote, promote fully or retry | the step fails before anything reaches the cluster or Git |
| before an image push | an image step that would push is about to be handed to a runner | the step fails before any runner sees it |

Rollback steps and aborts are never judged: they return to what ran before, which is what a
failure needs. A step whose policies pass, and a run no policy governs, run exactly as before.

**The rule catalog.**

| Rule | Judged | Passes when |
|---|---|---|
| `library_versions: { pinned: tag \| commit }` | run start | every shared library the run uses (and every override) resolved to a tag or a full commit id (`commit`: only a commit), never a branch |
| `required_templates: [ { library, template, versions } ]` | run start | a step uses each template of each library (the server's name for it), at a version matching `versions` that is a tag or a commit |
| `step_images: { digest, registries }` | run start | every image a step or container runs in is pinned by digest (`name@sha256:…`) and/or its repository matches `registries` (`*` within a part, a final `/**` any depth; `alpine` is `docker.io/library/alpine`) |
| `max_timeout: 2h` | run start | no step's timeout is longer (approvals' waits are not counted) |
| `forbid_secrets: { events: [...] }` | run start | a run of one of these events (`pull_request`, `draft`) that may read secrets has no step that asks for one |
| `tests: { min }` | before deploying | the steps the deploy step needs (directly or not) reported at least `min` test results (default 1), none failed or errored |
| `findings: { max_severity, per_tool, scanners }` | before deploying | those steps reported findings (no report is not "no findings"), none worse than `max_severity` (`none`, `low`, `medium`, `high`, `critical`; `unknown` counts only against `none`), none from a tool worse than its `per_tool` severity, and every tool in `scanners` reported |
| `sbom: { min_components }` | before deploying | one of those steps made an SBOM of at least `min_components` (default 1) |
| `deployed_images: { digest, built_by_run, signed, scanned }` | before deploying | every image the deployment writes is pinned by digest, was built by an image step before it in this run (its digest or a platform's), was signed, and was scanned without its scan failing it. A deployment that writes no image reference fails the rule |
| `approvals: { min }` | before deploying | at least `min` people (default 1) other than the person who started the run approved an approval step before it |
| `refs: { branches, tags }` | before deploying | the run is of a branch or tag matching the patterns (a pull-request or draft run is of neither) |
| `image_push: { scan, sign, sbom, provenance, registries }` | before an image push | the step scans with `mode: enforce` and a `fail_on` at least as strict as `scan`, signs (`runner`: with the runner's own key; `any`), makes an SBOM and provenance, and pushes (and writes its cache) only to the listed registries. A step that pushes nothing passes |

Evidence is what the run's events say: the summaries kept with each step attempt (test totals,
findings by severity and by tool, the SBOM, what an image step built and signed), approvals and who
gave them, and the resolved values of the deploy step's changes. A step a re-run reused is judged
by what it reported in the run where it ran. Evidence that is missing never passes a rule.

**Scope and modes.** `scope` narrows a policy to `projects`, `environments` (deploy rules only: a
policy scoped to environments may not have rules of other points), `branches` and `events`
(patterns as elsewhere; an empty list matches everything; a run without a branch does not match a
policy scoped to branches). A policy in `enforce` mode stops the step or the run; in `report` mode
it is judged, recorded and shown the same way, as a warning, and never changes an outcome. Start
new policies in report mode, watch their verdicts on the Policy page, then enforce them.

**Every decision is recorded.** Each decision is a `policy.evaluated` event in the run's log, with
the policies as judged (their source and version), the facts judged, the waivers considered and
the time, so it can be reproduced from the log alone; `launchway policy run <run-id>` prints it.
The run's **Policy** tab shows each decision rule by rule: what was found, what would satisfy the
rule, links to the evidence (the Tests, Security, Images and Artifacts tabs, approval steps), and
the waiver that passed a rule. The **Policy** page lists the policies in force, where each comes
from and at which version, their scope, mode and rules, and recent verdicts of the runs you may see.

**Waivers.** A waiver lets one rule of one policy pass for a project, optionally only in one run,
one environment, or (for `findings`) for one finding by its fingerprint, until it expires. A reason
and an expiry are required (at most `policy.waivers.max_days`, default 30). Waivers are a log of
grants and revocations, written to the Logbook (`policy.waive`, `policy.revoke_waiver`), shown
wherever the verdict they changed is, and expire on their own: a decision made after expiry does
not count them, even in a run that started before. A rule is judged again when the run is resumed,
so the usual path is: grant a waiver, then resume. Who may waive:

```yaml
policy:
  waivers:
    allow: admin      # admin (the default), operator or none: the role needed on the project
    max_days: 30
```

A deploy rule needs that role on the environment too (`environments:` on access bindings): a
waiver that names no environment needs it from a binding not limited to environments. A limited
API token never grants more than its limits. On an environment marked `protected: true`, the person
who started a run may not waive its deploy gate (unless `approvals.allow_self` lets people approve
their own runs): a waiver of theirs for the run is refused, and a project-wide waiver they granted is
not applied to their runs there. A policy source that cannot be loaded cannot be waived.

**Policies from Git.** A source is one of the server's [libraries](libraries.md), pinned to a
tag or a full commit id (a branch is refused), and a directory of `*.yaml` (or `*.yml`) files in it, including its subdirectories, each with a
`policies:` list as above:

```yaml
libraries:
  - { name: org-policies, repo: https://git.example.com/platform/policies.git }
policy:
  sources:
    - name: org
      library: org-policies
      version: v3            # a tag, or a full commit id
      path: policies         # the default
      projects: ["*"]        # the projects it may govern; the default is every project
      refresh: 1m            # how often servers look at the tag again
      stale_after: 1h        # how long a version loaded earlier is used while the repository cannot be reached
```

Changing policies is then a reviewed commit and a new tag, and moving `version` in the
configuration. Every server loads each source and stores what it loaded in the database; every
server judges from what is stored, so all of them reach the same verdict from the same inputs.
Policy names must be unique across the configuration and the sources.

**Failing closed.** A source governs the projects it covers whatever is in it, so when its
policies cannot be known, every run of those projects is refused at every policy point (run start,
deploying, pushing an image), naming the source and the reason, and the Policy page shows the
source as failing closed. That happens when no server has loaded it yet (just after it is added),
when the version does not exist or is a branch, when a file does not parse or uses a rule this
server does not know (the message names the file and line), when two sources define the same
policy, when the version stored is not the one this server is configured with (during a change of
`version`, until a server with the new setting loads it), or when it has not been confirmed for
longer than `stale_after` because the repository cannot be reached. A short outage of the
repository does not refuse anything: the version loaded before stays in use until then. Policies in
the server's own configuration are checked when the file is read: a mistake stops the server from
starting, with the file and line.

**What policies do not protect against.** They judge what Launchway does: a person with access to
the cluster, the GitOps repository or the registry can still change what runs without Launchway,
and Argo CD can sync a commit Launchway did not make. Facts come from what steps report: a step
that writes a test report claiming tests passed, or a scanner configured to find nothing, satisfies
the rules as far as the report goes (keep scanning steps in shared templates that
`required_templates` asks for). A run started by a push records the commit's author as its starter,
which anyone can set in Git. Running steps unrestricted is the runner's setting, never the
pipeline's, so it is not a rule. Signatures are recorded by the image step, not verified again at
deploy time.

**API and commands.** `GET /api/v1/policies` (the policies in force, their sources, recent verdicts
of runs you may see), `GET /api/v1/runs/{id}/policy` (a run's decisions and the waivers that apply),
`GET /api/v1/waivers?project=&active=1`, `POST /api/v1/projects/{name}/waivers` with `{"policy",
"rule", "reason", "expires_in_hours", "run", "environment", "finding"}`, and
`POST /api/v1/projects/{name}/waivers/{waiver}/revoke` with an optional `{"reason"}`. On the command
line: `launchway policy list`, `launchway policy run <run-id>`, `launchway waivers list
[--project P] [--active]`, `launchway waivers grant <project> --policy P --rule R --reason "..."
--expires 72h [--run R] [--environment E] [--finding F]` and `launchway waivers revoke <project> <id>`.
With the Helm chart, policies are `config.policy` in the values (unset by default). Policies need
schema 16: configure them only once every server runs a release that knows them, and take them out
before rolling back (see [docs/development/migrations.md](../development/migrations.md)).
