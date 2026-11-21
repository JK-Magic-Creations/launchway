// Browser fixture for the pipeline graph: fakes run responses for Launchway's own pipeline shape mid-run, a wide fan-out, a diamond,
// a long chain and a failed step with skipped dependents, then screenshots the run page's pipeline (light and dark, 1440 and 390 wide),
// the pipeline definition view, and the compact launch line with a station of parallel steps.
// Run: scripts/a11y-server.sh sh -c 'CHROME=... PLAYWRIGHT_CORE=web/node_modules/playwright-core/index.mjs SHOTS_DIR=out node scripts/ui-fixtures/pipeline-graph.mjs'
const { chromium } = await import(process.env.PLAYWRIGHT_CORE ?? "playwright-core");
const BASE = process.env.LAUNCHWAY_A11Y_URL ?? process.env.LAUNCHWAY_URL ?? "http://127.0.0.1:18124";
const OUT = (process.env.SHOTS_DIR ?? "shots/pipeline-graph") + "/";
const NOW = Date.now();
const S = 1000;

const st = (run, id, position, status, needs, extra = {}) => ({
  run_id: run, id, position, status, attempt: 1, queued_at: NOW - 200 * S,
  started_at: ["pending", "queued", "skipped"].includes(status) ? 0 : NOW - (status === "running" ? 71 : 120) * S,
  finished_at: ["succeeded", "failed"].includes(status) ? NOW - 60 * S : 0,
  exit_code: status === "failed" ? 2 : null, error: status === "failed" ? "exit code 2" : "", skip_kind: status === "skipped" ? "not_needed" : "", runner: "", outputs: {}, excerpt: "",
  retry: 0, timeout: "15m", deadline: 0, decided_by: "", decided_at: 0, comment: "", inputs: [], needs, ...extra });

function levelsOf(steps) {
  const depth = {};
  const d = (s) => depth[s.id] ??= Math.max(-1, ...s.needs.map((n) => d(steps.find((x) => x.id === n)))) + 1;
  steps.forEach(d);
  const out = [];
  steps.forEach((s) => (out[depth[s.id]] ??= []).push(s.id));
  return out;
}

const own = (run) => [
  ["check-code", [], "succeeded"], ["test", ["check-code"], "running"], ["security-go", ["check-code"], "running"], ["security-web", ["check-code"], "running"],
  ["build-ui", ["test"], "pending"], ["image", ["build-ui", "security-go", "security-web"], "pending", { kind: "image" }], ["deploy-sandbox", ["image"], "pending", { kind: "deploy", environment: "sandbox" }],
  ["deploy-dev", ["deploy-sandbox"], "pending", { kind: "deploy", environment: "dev" }],
].map(([id, needs, status, extra], i) => st(run, id, i, status, needs, extra));
const fan = (run) => [st(run, "checkout", 0, "succeeded", []),
  ...Array.from({ length: 8 }, (_, i) => st(run, `shard-${i + 1}`, i + 1, i < 3 ? "succeeded" : i < 6 ? "running" : "queued", ["checkout"])),
  st(run, "merge-reports", 9, "pending", Array.from({ length: 8 }, (_, i) => `shard-${i + 1}`))];
const diamond = (run) => [st(run, "build", 0, "succeeded", []), st(run, "unit-tests", 1, "succeeded", ["build"]), st(run, "lint", 2, "running", ["build"]), st(run, "package", 3, "pending", ["unit-tests", "lint"])];
const chain = (run) => Array.from({ length: 16 }, (_, i) => st(run, `stage-${i + 1}`, i, i < 5 ? "succeeded" : i === 5 ? "running" : "pending", i ? [`stage-${i}`] : []));
const folded = (run) => Array.from({ length: 16 }, (_, i) => st(run, `stage-${i + 1}`, i, i < 15 ? "succeeded" : "running", i ? [`stage-${i}`] : []));
const failed = (run) => [st(run, "check-code", 0, "succeeded", []), st(run, "test", 1, "failed", ["check-code"]), st(run, "security-go", 2, "succeeded", ["check-code"]), st(run, "security-web", 3, "succeeded", ["check-code"]),
  st(run, "build-ui", 4, "skipped", ["test"]), st(run, "image", 5, "skipped", ["build-ui", "security-go", "security-web"], { kind: "image" }), st(run, "approve", 6, "skipped", ["image"], { kind: "approval" })];
const templated = (run) => [st(run, "check", 0, "succeeded", []), st(run, "build/compile", 1, "succeeded", ["check"], { group: "build" }), st(run, "build/pack", 2, "running", ["build/compile"], { group: "build" }),
  st(run, "docs", 3, "succeeded", ["check"]), st(run, "approve", 4, "waiting", ["build/pack", "docs"], { kind: "approval", message: "Release to production?" }), st(run, "ship", 5, "pending", ["approve", "check"])];

const SCENARIOS = {
  own: { steps: own, status: "running", name: "launchway", message: "Add the pipeline graph" },
  fanout: { steps: fan, status: "running", name: "sharded-tests", message: "Run the suite in eight shards" },
  diamond: { steps: diamond, status: "running", name: "diamond", message: "Build, then check two ways, then package" },
  chain: { steps: chain, status: "running", name: "long-chain", message: "A long serial pipeline" },
  chainfold: { steps: folded, status: "running", name: "long-chain", message: "A long serial pipeline, folded while quiet" },
  failed: { steps: failed, status: "failed", name: "launchway", message: "Break a test" },
  mixed: { steps: templated, status: "running", name: "templated", message: "Template group, an approval and a long skip", groups: [{ id: "build", uses: "go/build", library: "go", template: "build", steps: ["build/compile", "build/pack"] }] },
};

function runDetail(key, sc) {
  const id = "run_g_" + key;
  const steps = sc.steps(id);
  return { id, project: "shop-api", number: 40, pipeline: sc.name, status: sc.status, cancel_requested: false, event: "push", ref: "refs/heads/main", branch: "main", tag: "", sha: "c0ffee0123456789",
    actor: "sam.rivera", author: "Sam Rivera", message: sc.message, created_at: NOW - 200 * S, started_at: NOW - 130 * S, finished_at: sc.status === "failed" ? NOW - 60 * S : 0,
    steps: steps.map((s) => ({ id: s.id, status: s.status })), waiting: [], draft: false, needs_newer_server: false, source: { kind: "git", url: "https://git.example/shop-api.git" },
    graph: { name: sc.name, on: {}, steps: steps.map((s) => ({ id: s.id, needs: s.needs, group: s.group, kind: s.kind })), groups: sc.groups }, levels: levelsOf(steps),
    reruns: [], not_understood: [], trigger_detail: null, step_list: steps, deployments: [], allowed_failures: 0, allowed_failure_steps: [],
    resume: { allowed: true, first_failed: steps.find((s) => s.status === "failed")?.id ?? "" },
    actions: [{ name: "cancel", available: true, who: "operator" }, { name: "rerun", available: true, who: "operator" }] };
}

const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ["--no-sandbox"] });
async function open(url, scheme, w, h, mock) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.log("pageerror", String(e).slice(0, 500)));
  await page.route(/\/api\/v1\/runs\/run_g_/, (r) => {
    const m = r.request().url().match(/run_g_([a-z]+)(\/.*)?$/);
    if (m && !m[2]) return r.fulfill({ json: runDetail(m[1], SCENARIOS[m[1]]) });
    if (/\/variables/.test(m?.[2] ?? "")) return r.fulfill({ json: { run_id: "x", variables: [], words: {} } });
    if (/\/logs/.test(m?.[2] ?? "")) return r.fulfill({ json: { lines: [] } });
    if (/\/events/.test(m?.[2] ?? "")) return r.fulfill({ json: { events: [] } });
    return r.fulfill({ json: [] });
  });
  if (mock) await mock(page);
  await page.goto(BASE + url, { waitUntil: "networkidle" }).catch(() => {});
  await page.waitForTimeout(700);
  return { page, close: () => ctx.close() };
}
const sizes = [["1440", 1440, 900], ["390", 390, 844]];
for (const scheme of ["light", "dark"]) {
  for (const [label, w, h] of sizes) {
    for (const key of Object.keys(SCENARIOS)) {
      const { page, close } = await open("/runs/run_g_" + key, scheme, w, h);
      const box = await page.locator("section.runline-sheet").evaluate((el) => { const r = el.getBoundingClientRect(); return { x: r.x + scrollX, y: r.y + scrollY, width: r.width, height: r.height }; });
      await page.screenshot({ path: `${OUT}run-${key}-${scheme}-${label}.png`, fullPage: true, clip: box });
      if (key === "own") await page.screenshot({ path: `${OUT}run-own-page-${scheme}-${label}.png` });
      await close();
    }
    // The pipeline definition view: the same graph before any run.
    const def = await open("/projects/shop-api/pipeline", scheme, w, h, (page) => page.route(/\/api\/v1\/validate/, (r) => r.fulfill({ json: {
      ok: true, graph: { name: "launchway", on: { push: { branches: ["main"] } }, steps: own("x").map((s) => ({ id: s.id, needs: s.needs, image: "docker.io/library/golang:1.27", run: "go test", retry: 0, timeout: "20m", kind: s.kind })) }, levels: levelsOf(own("x")) } })));
    await def.page.screenshot({ path: `${OUT}definition-${scheme}-${label}.png`, fullPage: true });
    await def.close();
    // The compact launch line on Home, with a station that stands for three parallel steps (hover shows each).
    const home = await open("/", scheme, w, h, (page) => page.route(/\/api\/v1\/launch-lines/, (r) => r.fulfill({ json: [{ run: "run_g_own", number: 40, project: "shop-api", message: "Add the pipeline graph", actor: "sam.rivera", ref: "main", sha: "c0ffee01", status: "running",
      created_at: NOW - 200 * S, finished_at: 0, tone: "run", now: "3 checks are running at once",
      stations: [{ key: "commit", label: "Commit", state: "done", detail: "c0ffee01" },
        { key: "checks", label: "Checks", state: "running", detail: "check-code, security-go, security-web", parallel: { total: 4, together: 3, done: 2, running: 1, waiting: 0, failed: 0, skipped: 0, idle: 1, steps: [{ id: "check-code", state: "done" }, { id: "security-go", state: "running" }, { id: "security-web", state: "done" }, { id: "lint", state: "idle" }] } },
        { key: "tests", label: "Tests", state: "running", detail: "test is running", parallel: { total: 3, together: 3, done: 1, running: 2, waiting: 0, failed: 0, skipped: 0, idle: 0, steps: [{ id: "test-a", state: "done" }, { id: "test-b", state: "running" }, { id: "test-c", state: "running" }] } },
        { key: "package", label: "Package", state: "idle", detail: "Not started" }] }] })));
    const pin = home.page.locator(".lline .ls .pstack").first();
    if (await pin.count()) { await pin.hover(); await home.page.waitForTimeout(200); }
    await home.page.screenshot({ path: `${OUT}home-line-${scheme}-${label}.png` });
    await home.close();
  }
}
await browser.close();
