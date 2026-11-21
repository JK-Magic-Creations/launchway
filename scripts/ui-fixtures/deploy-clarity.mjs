// Browser fixture for the deploy-clarity pages: fakes the run and control API responses (a failed smoke analysis with a job metric, a refused resume, a command the runner may not apply, one it may) and screenshots light and dark at 1440 and 390 wide.
// Run: LAUNCHWAY_URL=http://127.0.0.1:18123 CHROME=/path/to/chrome PLAYWRIGHT_CORE=/path/to/playwright-core/index.mjs SHOTS_DIR=out node scripts/ui-fixtures/deploy-clarity.mjs
const { chromium } = await import(process.env.PLAYWRIGHT_CORE ?? "playwright-core");
const BASE = process.env.LAUNCHWAY_URL ?? "http://127.0.0.1:18123";
const OUT = (process.env.SHOTS_DIR ?? "shots/deploy-clarity") + "/";
const T = 1763700000000;
const why = "this deploy runner may not change the status of rollout shop/shop, so it cannot apply promote, promote full, abort or retry; an administrator must list the rollout in the chart value delivery.deployRunner.rbac.controlRollouts (for example {shop: [shop]})";
const analysis = { name: "shop-7-smoke", phase: "Failed", namespace: "shop", pod_hash: "h2", metrics: [{ name: "smoke", phase: "Failed", count: 20, successful: 19, failed: 1, inconclusive: 0, error: 0, provider: "job", interval: "30s", failure_limit: 0,
  measurements: [{ phase: "Successful", at: T + 30000 }, { phase: "Successful", at: T + 60000 }, { phase: "Failed", message: "Job failed: curl: (6) Could not resolve host: new.shop.svc", at: T + 90000 }],
  job: { name: "smoke-7", namespace: "shop" }, job_log: { note: "the job's log is in shop/smoke-7; this runner may not read it" } }] };
const summary = 'The dev deploy was stopped by its "smoke" analysis: smoke failed 1 of 20 measurements, and it allows 0 (Job failed: curl: (6) Could not resolve host: new.shop.svc).';
const st = (id, position, status, extra = {}) => ({ run_id: "run_fx", id, position, status, attempt: 1, queued_at: T, started_at: T, finished_at: T + 1000, exit_code: null, error: "", skip_kind: "", runner: "build-1", outputs: {}, excerpt: "", retry: 0, timeout: "10m", ...extra, deadline: 0, decided_by: "", decided_at: 0, comment: "", inputs: [] });
const dep = (ctl) => ({ id: "dep_1", run_id: "run_fx", run_number: 7, project: "shop", step: "deploy-dev", environment: "dev", service: "shop", target: "cluster-a", application: "shop", mode: "write_back", version: "v1.3.0", previous_version: "v1.2.3", changed: true, phase: "rollout_paused", detail: "", verdict: "", opened_at: T, observed_at: Date.now(), holding: true, timeline: [], commands: ctl.commands ?? [],
  snapshot: { app: { found: true, sync: "Synced", health: "Suspended", source: 0, automated: true }, rollout: { found: true, phase: "Paused", generation: 1, observed: true, strategy: "canary", step: 1, steps: 4, step_names: ["weight 20%", "pause 2m", "weight 60%", "weight 100%"], canary_weight: 20, paused: true, pause_reasons: ["CanaryPauseStep"], pause_until: Date.now() + 95000, aborted: false, replicas: 4, ready: 4, updated: 1, available: 4 }, analysis: [{ ...analysis, phase: "Running", metrics: [{ ...analysis.metrics[0], phase: "Running", failed: 0, count: 3, measurements: analysis.metrics[0].measurements.slice(0, 2), job_log: undefined }] }] } });
const graph = { name: "shop", on: {}, steps: [{ id: "build", needs: [] }, { id: "deploy-sandbox", needs: ["build"], kind: "deploy", environment: "sandbox" }, { id: "deploy-dev", needs: ["deploy-sandbox"], kind: "deploy", environment: "dev" }, { id: "rollback-dev", needs: ["deploy-dev"], kind: "rollback", deployment: "deploy-dev" }] };
const base = { id: "run_fx", project: "shop", number: 7, pipeline: "shop", cancel_requested: false, event: "push", ref: "refs/heads/main", branch: "main", tag: "", sha: "a".repeat(40), actor: "ann", message: "Add the smoke test", author: "Ann", created_at: T, started_at: T, finished_at: T + 600000, waiting: [], draft: false, needs_newer_server: false, source: { kind: "git", url: "https://git.example/shop.git" }, graph, levels: [["build"], ["deploy-sandbox"], ["deploy-dev"], ["rollback-dev"]], reruns: [], not_understood: [], trigger_detail: null };
const failedRun = { ...base, status: "failed", steps: [], 
  step_list: [st("build", 0, "succeeded", { outputs: { digest: "sha256:abc123abc123abc123abc123abc123abc123", tag: "v1.3.0" }, kind: "image" }), st("deploy-sandbox", 1, "succeeded", { kind: "deploy", environment: "sandbox" }),
    st("deploy-dev", 2, "failed", { kind: "deploy", environment: "dev", error: "the rollout was aborted: RolloutAborted: Metric \"smoke\" assessed Failed", reason: summary, cause: { kind: "analysis_failed", summary, environment: "dev", analysis, next: { kind: "rolled_back", step: "rollback-dev", version: "v1.2.3", summary: "Rolled back to v1.2.3 by rollback-dev." } } }),
    st("rollback-dev", 3, "succeeded", { kind: "rollback", deployment: "deploy-dev", environment: "dev" })],
  deployments: [{ ...dep({}), phase: "rollout_aborted", verdict: "rolled_back", closed_at: T + 500000, snapshot: { rollout: { found: true, phase: "Degraded", aborted: true, generation: 1, observed: true, steps: 4, paused: false, replicas: 4, ready: 4, updated: 0, available: 4 }, analysis: [analysis] } }],
  resume: { allowed: false, reason: "deploy-sandbox made a deployment in this run", rerun_from: "deploy-dev", first_failed: "deploy-dev" },
  actions: [{ name: "cancel", available: false, reason: "the run has already finished", who: "operator" }, { name: "resume", available: false, reason: "deploy-sandbox made a deployment in this run, and a half-done deployment is never continued, so this run cannot be resumed. Use \"Re-run from deploy-dev\"", instead: "rerun from deploy-dev", who: "operator" }, { name: "rerun", available: true, who: "operator" }] };
const runningRun = (ctl, controls) => ({ ...base, status: "running", finished_at: 0, steps: [], step_list: [st("build", 0, "succeeded", { kind: "image", outputs: { digest: "sha256:abc123abc123abc123abc123abc123abc123" } }), st("deploy-sandbox", 1, "succeeded", { kind: "deploy" }), st("deploy-dev", 2, "waiting", { kind: "deploy", environment: "dev", wait_kind: "observe", finished_at: 0 }), st("rollback-dev", 3, "pending", { kind: "rollback", deployment: "deploy-dev", attempt: 0 })], deployments: [dep(ctl)], actions: [{ name: "cancel", available: true, who: "operator" }, { name: "resume", available: false, reason: "only a failed or canceled run can be resumed", who: "operator" }, { name: "rerun", available: false, reason: "only a finished run can be re-run", who: "operator" }] });
const failedCmd = { id: "cmd_1", deployment: "dep_1", run_id: "run_fx", step: "deploy-dev", action: "promote-full", actor: "ann", origin: "person", expect: { pod_hash: "h2", step: 1, paused: true }, state: "done", result: "failed", detail: why, tries: 1, at: T, done_at: T + 5000 };
const cur = {};
const ctlNo = { environment: "dev", protected: false, withdraw: true, withdraw_reason: "", controls: ["promote", "promote-full", "abort", "retry"].map((action) => ({ action, available: false, runner_cannot: true, reason: why })) };
const ctlYes = { environment: "dev", protected: false, withdraw: true, withdraw_reason: "", controls: [{ action: "promote", available: true }, { action: "promote-full", available: true }, { action: "abort", available: true }, { action: "retry", available: false, reason: "the rollout is not aborted" }] };
const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ["--no-sandbox"] });
const shots = [];
async function shoot(name, scenario, url, scheme, w, h) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.route(/\/api\/v1\/me$/, async (r) => { const j = await (await r.fetch()).json(); j.roles = { ...(j.roles||{}), shop: "operator" }; r.fulfill({ json: j }); });
  await page.route(/\/api\/v1\/runs\/run_fx$/, (r) => r.fulfill({ json: scenario.run }));
  await page.route(/\/api\/v1\/runs\/run_fx\/deployments\/dep_1\/controls/, (r) => r.fulfill({ json: scenario.controls ?? ctlNo }));
  await page.route(/\/api\/v1\/runs\/run_fx\/(events|logs)/, (r) => r.fulfill({ json: name.includes("x") ? { events: [], lines: [] } : { events: [], lines: [] } }));
  await page.goto(BASE + url, { waitUntil: "networkidle" }).catch(() => {});
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${OUT}${name}-${scheme}-${w}.png`, fullPage: true });
  const bad = await page.evaluate(() => [...document.querySelectorAll("button:not([disabled])")].map((b) => b.textContent.trim()).filter(Boolean));
  if (scheme==="light"&&w===1440) console.log(name, "enabled:", bad.filter((b)=>/Re-run|Resume|Promote|Abort|Retry|Cancel/.test(b)).join(" | "));
  await ctx.close();
}
for (const [scheme, w, h] of [["light", 1440, 900], ["dark", 1440, 900], ["light", 390, 844], ["dark", 390, 844]]) {
  await shoot("failed-run", { run: failedRun }, "/runs/run_fx?step=deploy-dev", scheme, w, h);
  await shoot("rollout-runner-cannot", { run: runningRun({ commands: [failedCmd] }), controls: ctlNo }, "/runs/run_fx?step=deploy-dev", scheme, w, h);
  await shoot("rollout-runner-can", { run: runningRun({}), controls: ctlYes }, "/runs/run_fx?step=deploy-dev", scheme, w, h);
}
await browser.close();
