// Browser fixture for rolling back after a deployment succeeded: fakes the environment, deployment, run and review API responses and screenshots the
// application page (the button, and the unavailable state with its reason), the confirmation, the rollback run and the needs-review list, light and dark.
// Run: LAUNCHWAY_URL=http://127.0.0.1:18124 CHROME=/path/to/chrome PLAYWRIGHT_CORE=/path/to/playwright-core/index.mjs SHOTS_DIR=out node scripts/ui-fixtures/rollback.mjs
const { chromium } = await import(process.env.PLAYWRIGHT_CORE ?? "playwright-core");
const BASE = process.env.LAUNCHWAY_URL ?? "http://127.0.0.1:18124";
const OUT = (process.env.SHOTS_DIR ?? "shots/rollback") + "/";
const T = Date.now() - 6 * 3600 * 1000;
const H = 3600 * 1000;

const target = (version, n, at, reverts, previous) => ({
  deployment: "dep_" + n, run: "run_" + n, run_number: n, version, at, previous,
  reverts: reverts.map(([v, rn]) => ({ deployment: "dep_" + rn, run: "run_" + rn, run_number: rn, version: v, at: T + rn * H / 4 })),
  consequence: `dev goes from v1.4.0 back to ${version}; ${reverts.length === 1 ? "the deployment of v1.4.0 (run #19) is" : `the ${reverts.length} deployments after it (runs ${reverts.map(([, rn]) => "#" + rn).join(", ")}) are`} reverted in Git, newest first; the database and configuration are not rolled back`,
  suggestions: [`If v1.4.0 included a database migration, check that it is backward compatible with ${version}: a rollback never undoes a database.`],
});
const targets = [target("v1.3.2", 18, T + H, [["v1.4.0", 19]], true), target("v1.3.1", 17, T - 5 * H, [["v1.4.0", 19], ["v1.3.2", 18]])];
const available = { name: "rollback", available: true, who: "operator", targets, risk: targets[0].consequence, suggestion: targets[0].suggestions[0] };
const unavailable = { name: "rollback", available: false, who: "operator", reason: "no deploy runner is serving target homelab right now, so nothing could revert the change and watch the result", would_help: "start or repair the deploy runner for target homelab (see Runners); it must report in before a rollback is offered" };
const queueable = { ...available, available: false, queueable: true, reason: "run #20 is deploying shop to dev right now and holds its lock, so a rollback cannot start until it ends", instead: "queue the rollback behind run #20 (it starts when run #20 ends, ahead of other waiting deployments), or cancel run #20 first" };

const rbBrief = { headline: "Emergency rollback of dev: v1.4.0 → v1.3.2, by otto: checkout fails for customers since 1.4.0", to_version: "v1.3.2", from_version: "v1.4.0", actor: "otto", reason: "checkout fails for customers since 1.4.0", emergency: true, needs_review: true };
const dep = (id, n, version, extra = {}) => ({ id, run_id: "run_" + n, run_number: n, project: "shop", step: "deploy-dev", environment: "dev", service: "shop", target: "homelab", application: "shop", mode: "write_back", version, changed: true,
  commit: "c0ffee" + n + "0".repeat(30), branch: "main", repo: "gitops", phase: "healthy", detail: "", verdict: "deployed", opened_at: T + n * H / 4, deployed_at: T + n * H / 4 + 60000, closed_at: T + n * H / 4 + 120000, holding: false,
  observed_at: Date.now(), observed_by: "deployer-1", snapshot: { app: { found: true, sync: "Synced", health: "Healthy", source: 0, automated: true } }, ...extra });
const history = (rolled) => [
  ...(rolled ? [dep("dep_21", 21, "v1.3.2", { mode: "rollback", previous_version: "v1.4.0", rollback: rbBrief, step: "rollback", run_id: "run_21", run_number: 21 })] : []),
  dep("dep_19", 19, "v1.4.0", { previous_version: "v1.3.2", actions: [available], rolled_back_by: rolled ? [{ id: "run_21", number: 21, actor: "otto", reason: "checkout fails for customers since 1.4.0" }] : undefined }),
  dep("dep_18", 18, "v1.3.2", { previous_version: "v1.3.1" }), dep("dep_17", 17, "v1.3.1", { previous_version: "v1.3.0" }),
];
const env = (rollback) => [{ name: "dev", target: "homelab", queue: "newest", projects: ["shop"], protected: true, observer: "deployer-1", observer_expires: Date.now() + 600000,
  services: [{ project: "shop", service: "shop", waiting: [], current: { deployment: "dep_19", version: "v1.4.0", run: "run_19", run_number: 19, since: T + 5 * H }, last: { ...dep("dep_19", 19, "v1.4.0", { previous_version: "v1.3.2", actions: [rollback] }) }, rollback }] }];

const st = (id, position, status, extra = {}) => ({ run_id: "run_21", id, position, status, attempt: 1, queued_at: T, started_at: T, finished_at: T + 60000, exit_code: null, error: "", skip_kind: "", runner: "", outputs: {}, excerpt: "", retry: 0, timeout: "15m", deadline: 0, decided_by: "", decided_at: 0, comment: "", inputs: [], ...extra });
const rollbackRun = (review) => ({ id: "run_21", project: "shop", number: 21, pipeline: "rollback", status: "succeeded", cancel_requested: false, event: "rollback", ref: "", branch: "", tag: "", sha: "", actor: "otto", author: "",
  message: rbBrief.headline, created_at: T + 5 * H, started_at: T + 5 * H, finished_at: T + 5 * H + 180000, steps: [], waiting: [], draft: false, needs_newer_server: false, source: { kind: "git", url: "https://git.example/shop.git" },
  graph: { name: "rollback", on: {}, steps: [{ id: "rollback", needs: [], kind: "rollback", environment: "dev", service: "shop" }] }, levels: [["rollback"]], reruns: [], not_understood: [], trigger_detail: null,
  step_list: [st("rollback", 0, "succeeded", { kind: "rollback", environment: "dev" })], deployments: [history(true)[0]],
  actions: [{ name: "cancel", available: false, reason: "the run has already finished", who: "operator" }, { name: "resume", available: false, reason: "this is a rollback run: a half-done rollback is never continued, and a stale one is not repeated. Ask for the rollback again from the environment or application page", who: "operator" }, { name: "rerun", available: false, reason: "this is a rollback run: a half-done rollback is never continued, and a stale one is not repeated. Ask for the rollback again from the environment or application page", who: "operator" }],
  rollback: { environment: "dev", service: "shop", headline: rbBrief.headline, to: { deployment: "dep_18", version: "v1.3.2", run: "run_18", run_number: 18, at: T + H }, from: { deployment: "dep_19", version: "v1.4.0", run: "run_19", run_number: 19 },
    reverts: [{ deployment: "dep_19", version: "v1.4.0", commit: "c0ffee190", run: "run_19", run_number: 19 }], reason: "checkout fails for customers since 1.4.0", actor: "otto", emergency: true, review_required: true, review,
    note: "A rollback restores the version only. It never restores a database or configuration." } });
const originalRun = { ...rollbackRun(undefined), id: "run_19", number: 19, pipeline: "shop", event: "push", message: "Add the discount code", actor: "ann", rollback: undefined, rolled_back_by: [{ id: "run_21", number: 21, actor: "otto", reason: "checkout fails for customers since 1.4.0" }],
  graph: { name: "shop", on: {}, steps: [{ id: "build", needs: [] }, { id: "deploy-dev", needs: ["build"], kind: "deploy", environment: "dev" }] }, levels: [["build"], ["deploy-dev"]],
  step_list: [st("build", 0, "succeeded", { run_id: "run_19" }), st("deploy-dev", 1, "succeeded", { run_id: "run_19", kind: "deploy", environment: "dev" })], deployments: [history(true)[1]],
  actions: [{ name: "cancel", available: false, reason: "the run has already finished", who: "operator" }, { name: "resume", available: false, reason: "only a failed or canceled run can be resumed", who: "operator" }, { name: "rerun", available: true, who: "operator" }] };
const review = (ack) => [{ run: "run_21", number: 21, project: "shop", environment: "dev", service: "shop", from_version: "v1.4.0", to_version: "v1.3.2", reason: "checkout fails for customers since 1.4.0", actor: "otto", at: T + 5 * H, acknowledge: ack }];

const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ["--no-sandbox"] });
async function shoot(name, scenario, url, scheme, w, h, act) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.route(/\/api\/v1\/me$/, async (r) => { const j = await (await r.fetch()).json(); j.roles = { ...(j.roles || {}), shop: "operator" }; r.fulfill({ json: j }); });
  await page.route(/\/api\/v1\/environments$/, (r) => r.fulfill({ json: env(scenario.action) }));
  await page.route(/\/api\/v1\/deployments\?/, (r) => r.fulfill({ json: history(!!scenario.rolled) }));
  await page.route(/\/api\/v1\/rollbacks\/review/, (r) => r.fulfill({ json: scenario.reviews ?? [] }));
  await page.route(/\/api\/v1\/runs\/run_21$/, (r) => r.fulfill({ json: rollbackRun(scenario.review) }));
  await page.route(/\/api\/v1\/runs\/run_19$/, (r) => r.fulfill({ json: scenario.rolled ? originalRun : { ...originalRun, rolled_back_by: undefined } }));
  await page.route(/\/api\/v1\/runs\/run_(19|21)\/(events|steps)/, (r) => r.fulfill({ json: { events: [], lines: [] } }));
  await page.goto(BASE + url, { waitUntil: "networkidle" }).catch(() => {});
  await page.waitForTimeout(800);
  if (act) await act(page);
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${OUT}${name}-${scheme}-${w}.png`, fullPage: true });
  await ctx.close();
}
const openDialog = async (page) => { await page.getByRole("button", { name: /Roll back to v1\.3\.2/ }).first().click(); await page.getByRole("dialog").waitFor(); await page.getByRole("textbox").fill("checkout fails for customers since 1.4.0"); };
const picker = async (page) => { await page.getByRole("button", { name: "Roll back to…" }).first().click(); await page.getByRole("dialog").waitFor(); };
const ack = { name: "acknowledge", available: true, who: "operator" };
for (const [scheme, w, h] of [["light", 1440, 900], ["dark", 1440, 900], ["light", 390, 844], ["dark", 390, 844]]) {
  await shoot("application-button", { action: available }, "/deployments/applications/shop", scheme, w, h);
  await shoot("confirmation", { action: available }, "/deployments/applications/shop", scheme, w, h, openDialog);
  await shoot("picker", { action: available }, "/deployments/applications/shop", scheme, w, h, picker);
  await shoot("unavailable", { action: unavailable }, "/deployments/applications/shop", scheme, w, h);
  await shoot("queue-behind", { action: queueable }, "/deployments/applications/shop", scheme, w, h);
  await shoot("rollback-run", { action: available, rolled: true, review: undefined }, "/runs/run_21", scheme, w, h);
  await shoot("original-run", { action: available, rolled: true }, "/runs/run_19?step=deploy-dev", scheme, w, h);
  await shoot("review-list", { action: available, rolled: true, reviews: review(ack) }, "/deployments/environments/dev", scheme, w, h);
  await shoot("review-list-not-you", { action: available, rolled: true, reviews: review({ ...ack, available: false, reason: "you asked for this rollback, so someone else must review it (approvals.allow_self is off)" }) }, "/deployments/environments/dev", scheme, w, h);
}
await browser.close();
