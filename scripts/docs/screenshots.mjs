// Takes the screenshot tour of a running demo server (see
// scripts/docs-screenshots.sh, which sets the environment below).
//
//   LAUNCHWAY_DOCS_URL     the server, such as http://127.0.0.1:18160
//   LAUNCHWAY_DOCS_OUT     where the PNG files go
//   LAUNCHWAY_DOCS_RUNS    the runs.env that seed-demo.sh wrote
//   LAUNCHWAY_DOCS_PW      a directory with playwright installed
//   LAUNCHWAY_DOCS_CHROME  a Chromium to use (optional)
//
// Every shot is taken at 1440x900 in light mode (NAME-light.png) and dark
// mode (NAME-dark.png); the UI follows prefers-color-scheme.
import { createRequire } from "node:module";
import { readFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

const env = (k) => {
  const v = process.env[k];
  if (!v) throw new Error(`${k} is not set`);
  return v;
};
const URL_ = env("LAUNCHWAY_DOCS_URL");
const OUT = env("LAUNCHWAY_DOCS_OUT");
const { chromium } = createRequire(join(env("LAUNCHWAY_DOCS_PW"), "/"))("playwright");
const runs = Object.fromEntries(readFileSync(env("LAUNCHWAY_DOCS_RUNS"), "utf8").trim().split("\n").map((l) => l.split("=")));

const api = async (method, path, body) => {
  const r = await fetch(URL_ + path, {
    method, headers: { "X-Launchway-CSRF": "1", "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${await r.text()}`);
  return r.json();
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, ms = 120000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await sleep(250);
  }
}

const browser = await chromium.launch({ executablePath: process.env.LAUNCHWAY_DOCS_CHROME || undefined });
const themes = ["light", "dark"];
let failures = 0;

async function shot(name, path, prep, { themesFor = themes, full = false } = {}) {
  for (const theme of themesFor) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme, reducedMotion: "reduce" });
    const page = await ctx.newPage();
    try {
      await page.goto(URL_ + path);
      await page.waitForSelector(".page :is(h1, h2)", { timeout: 15000 });
      if (prep) await prep(page, theme);
      await page.waitForTimeout(700);
      await page.screenshot({ path: join(OUT, `${name}-${theme}.png`), fullPage: full });
      console.log(`  ${name}-${theme}.png`);
    } catch (e) {
      failures++;
      console.error(`  ${name}-${theme}: ${e.message}`);
      await page.screenshot({ path: join(OUT, "..", `failed-${name}-${theme}.png`) }).catch(() => {});
    } finally {
      await ctx.close();
    }
  }
}
const text = (page, t) => page.getByText(t, { exact: false }).filter({ visible: true }).first().waitFor({ timeout: 15000 });

// Projects.
await shot("projects", "/projects", (p) => text(p, "web-frontend"));

// A run as it happens: start one, open its end-to-end tests while they run.
async function liveRun(page) {
  const run = await api("POST", "/api/v1/projects/web-frontend/runs", { ref: "main" });
  await until("e2e to run", async () => {
    const r = await api("GET", `/api/v1/runs/${run.id}`);
    return r.steps.some((s) => s.id.startsWith("e2e[") && s.status === "running");
  });
  await page.goto(`${URL_}/runs/${run.id}?step=${encodeURIComponent("e2e[browser=firefox]")}`);
  await text(page, "✓");
  await page.waitForTimeout(1500);
  return run;
}
await shot("run-live", "/", liveRun);

// The README's picture: a finished run of web-frontend, one step open.
await shot("readme-hero", `/runs/${runs.WEB_FIXED}?step=unit-tests`, async (p) => { await text(p, "Component and unit tests"); await text(p, "passed"); await p.waitForTimeout(1500); }, { themesFor: ["light"] });

// A failed run: the reason, and Resume from the failed step.
await shot("run-failed", `/runs/${runs.APP_FAILED}?step=smoke-test`, (p) => text(p, "Resume from smoke-test"));

// The tabs of a run.
await shot("run-tests", `/runs/${runs.WEB_FAILED}?tab=tests`, (p) => text(p, "offers the next five working days"));
await shot("run-security", `/runs/${runs.APP_DONE}?tab=security`, (p) => text(p, "CVE-2019-11324"));
await shot("run-artifacts", `/runs/${runs.APP_DONE}?tab=artifacts`, (p) => text(p, "binary"));

// A run of a shared library's template: its steps framed as one group.
await shot("library-run", `/runs/${runs.PAY_DONE}?step=release%2Fbuild`, (p) => text(p, "Built sha256"));

// Approvals waiting for a person.
await shot("approvals", "/approvals", (p) => text(p, "payments-api"));

// Starting a run: the branch and the pipeline's inputs.
await shot("start-run", "/projects", async (p) => {
  const card = p.locator("section.project").filter({ has: p.locator("h2", { hasText: /^demo-app$/ }) });
  await card.getByRole("button", { name: "Start a run" }).click();
  await text(p, "Start a run of demo-app");
  await p.waitForTimeout(1500);
});

// The pipeline editor: an unsaved change and the graph it makes; then a
// mistake in the text, and the problems it causes.
await shot("editor", "/projects/demo-app/pipeline?ref=main", async (p) => {
  const area = p.locator("textarea").first();
  await area.waitFor();
  const v = await area.inputValue();
  await area.fill(v.replace("Check the code style", "Check the code style and spelling"));
  await area.evaluate((el) => { el.setSelectionRange(0, 0); el.scrollTop = 0; });
  await text(p, "No problems");
  await p.locator(".graph, svg").first().waitFor();
  await p.waitForTimeout(1000);
});
await shot("editor-problems", "/projects/demo-app/pipeline?ref=main", async (p) => {
  const area = p.locator("textarea").first();
  await area.waitFor();
  const v = await area.inputValue();
  await area.fill(v.replace("    needs: unit-tests\n    retry: 1", "    needs: unit-test\n    retry: 1").replace("max_parallel: 2", "max_parallel: two"));
  await text(p, "3 problems");
  await p.getByRole("button", { name: "line 41" }).or(p.getByRole("link", { name: "line 41" })).first().click();
  await p.waitForTimeout(1000);
});

// The Playground: a pipeline drawn as you type.
await shot("playground", "/playground", async (p) => {
  const area = p.locator("textarea").first();
  await area.waitFor();
  await area.fill(readFileSync(new URL("./playground.yaml", import.meta.url), "utf8"));
  await p.waitForTimeout(1500);
});

// Test history of a project, findings by component, policy and audit.
await shot("project-tests", "/projects/web-frontend/tests", (p) => text(p, "disappears after five seconds"));
await shot("components", "/components?name=left-pad", (p) => text(p, "1.1.3"));
await shot("policy", "/policy", (p) => text(p, "pinned-libraries"));
await shot("audit", "/audit", (p) => text(p, "run.resume"));

// Frames for the animated tour: start a run in the UI and watch it.
if (process.env.LAUNCHWAY_DOCS_FRAMES !== "0") {
  const dir = join(OUT, "frames");
  mkdirSync(dir, { recursive: true });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "light" });
  const p = await ctx.newPage();
  let n = 0;
  const frame = async () => p.screenshot({ path: join(dir, String(n++).padStart(3, "0") + ".png") });
  try {
    await p.goto(URL_ + "/projects");
    await text(p, "web-frontend");
    await frame(); await frame();
    const card = p.locator("section.project").filter({ has: p.locator("h2", { hasText: /^web-frontend$/ }) });
    await card.getByRole("button", { name: "Start a run" }).click();
    await text(p, "Start a run of web-frontend");
    await p.waitForTimeout(800);
    await frame(); await frame(); await frame();
    await p.locator("dialog").getByRole("button", { name: /^Start/ }).last().click();
    await p.waitForURL(/\/runs\//, { timeout: 15000 });
    const id = p.url().split("/runs/")[1].split("?")[0];
    for (let i = 0; i < 80; i++) {
      await p.waitForTimeout(500);
      await frame();
      const r = await api("GET", `/api/v1/runs/${id}`);
      if (["succeeded", "failed", "canceled"].includes(r.status)) break;
    }
    await p.waitForTimeout(800); await frame(); await frame(); await frame(); await frame();
    console.log(`  ${n} frames`);
  } catch (e) {
    failures++;
    console.error(`  frames: ${e.message}`);
  } finally {
    await ctx.close();
  }
}

await browser.close();
if (failures) {
  console.error(`${failures} screenshot(s) failed`);
  process.exit(1);
}
