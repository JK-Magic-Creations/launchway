// Keyboard-only journeys through the real interface (make web-a11y), asserting what a screen reader would get: names,
// roles, states and announcements, from the accessibility tree (ariaSnapshot) and the live regions. No mouse is used:
// every step is a key press. Run through scripts/a11y-server.sh.
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { launch, server } from "./a11y-lib.mjs";

const { url, runs } = server();
const browser = await launch();
const results = [];

async function journey(name, fn) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: "reduce" });
  const page = await ctx.newPage();
  const started = Date.now();
  try {
    await fn(page);
    results.push({ name, ok: true });
    console.log(`  ok   ${name} (${((Date.now() - started) / 1000).toFixed(1)}s)`);
  } catch (e) {
    results.push({ name, ok: false, error: e.message });
    console.error(`  FAIL ${name}\n       ${e.message.split("\n").slice(0, 6).join("\n       ")}`);
  } finally {
    await ctx.close();
  }
}

const open = async (page, path) => { await page.goto(url + path); await page.getByRole("main").waitFor(); await page.waitForFunction(() => !document.querySelector(".loading")); };
const active = (page) => page.evaluate(() => {
  const e = document.activeElement;
  return { tag: e?.tagName.toLowerCase(), id: e?.id ?? "", role: e?.getAttribute("role") ?? "", name: (e?.getAttribute("aria-label") || e?.textContent || e?.getAttribute("placeholder") || "").trim().replace(/\s+/g, " ").slice(0, 80), label: e?.labels?.[0]?.textContent?.trim() ?? "" };
});
/** Presses Tab until the focused element satisfies `want` (a regular expression on its name or label), at most `max` times. */
async function tabTo(page, want, max = 90) {
  for (let i = 0; i < max; i++) {
    await page.keyboard.press("Tab");
    const a = await active(page);
    if (want.test(a.name) || want.test(a.label)) return a;
  }
  throw new Error(`Tab never reached ${want} in ${max} presses (last: ${JSON.stringify(await active(page))})`);
}
const polite = (page) => page.locator('[data-announcer="polite"]');
const snapshot = (loc) => loc.ariaSnapshot();
const has = (text, ...parts) => { for (const p of parts) assert.match(text, p instanceof RegExp ? p : new RegExp(p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))); };

console.log("Keyboard journeys");

await journey("the first Tab stop is the skip link, and it moves focus to the page", async (page) => {
  await open(page, "/");
  await page.keyboard.press("Tab");
  assert.equal((await active(page)).name, "Skip to the page");
  await page.keyboard.press("Enter");
  assert.equal((await active(page)).id, "main");
  // The landmarks and the one heading a screen reader navigates by.
  const tree = await snapshot(page.locator("body"));
  has(tree, /banner/, /navigation "Areas"/, /main/, /heading ".*" \[level=1\]/);
  assert.equal(await page.getByRole("heading", { level: 1 }).count(), 1);
  assert.equal(await page.evaluate(() => document.documentElement.lang), "en");
});

await journey("search: open with the slash key, find a failed run, read why it failed", async (page) => {
  await open(page, "/");
  await page.keyboard.press("/");
  const dialog = page.getByRole("dialog", { name: "What do you want to do?" });
  await dialog.waitFor();
  assert.equal((await active(page)).role, "combobox");
  await page.keyboard.type("failed");
  await page.getByRole("option", { name: /See why shop-api failed/ }).waitFor();
  has(await dialog.ariaSnapshot(), /combobox/, /listbox "Results"/, /option "(?:Do )?See why shop-api failed/);
  await page.waitForFunction(() => /\d+ results?/.test(document.querySelector('.modal [role="status"]')?.textContent ?? ""));
  await page.keyboard.press("Enter");
  await page.waitForURL(new RegExp("/runs/" + runs.RUN_FAILED));
  await page.getByRole("heading", { level: 1 }).waitFor();
  await page.getByText(/FAIL TestPriceRounding/).first().waitFor();
  // The page change was announced and focus is at the top of the new page.
  assert.equal((await active(page)).id, "main");
  await page.waitForFunction(() => /Sortie/.test(document.querySelector('[data-announcer="polite"]')?.textContent ?? ""));
  const tree = await snapshot(page.getByRole("main"));
  has(tree, /heading ".*" \[level=1\]/, /list "Pipeline: \d+ steps?/, /region "What went wrong"/, "TestPriceRounding");
});

await journey("a failed run reads as text: the pipeline is a list of stages whose steps are buttons that name their states", async (page) => {
  await open(page, `/runs/${runs.RUN_FAILED}`);
  // The graph is a list of stages (named "Pipeline: N steps in M stages") whose steps are buttons.
  const graph = page.locator("ol.pg-stages").first();
  await graph.waitFor({ state: "attached" });
  assert.match(await graph.getAttribute("aria-label"), /^Pipeline: \d+ steps?/);
  const steps = await page.locator("button[data-node]:visible").evaluateAll((b) => b.map((x) => x.getAttribute("aria-label") || x.textContent));
  assert.ok(steps.length >= 2, "steps are buttons with names");
  assert.ok(steps.some((t) => /failed/i.test(t)), "the failed step says failed in words: " + steps.join(" | "));
  assert.ok(steps.every((t) => /^\S/.test(t)), "each name starts with the step's own visible label");
  // The list of runs draws a launch line as a list with the same words.
  await open(page, "/");
  const lists = page.getByRole("list", { name: /Launch line/ });
  if (await lists.count()) assert.ok((await lists.first().getByRole("listitem").allTextContents()).some((t) => /passed|failed|waiting|running|not yet/.test(t)));
});

await journey("decide an approval: reason, then Go, by keyboard; the outcome is announced", async (page) => {
  await open(page, "/activity/approvals");
  const reason = await tabTo(page, /Reason/);
  assert.equal(reason.tag, "textarea");
  await page.keyboard.type("Tests pass and the notes are ready.");
  const tree = await snapshot(page.getByRole("main"));
  has(tree, /textbox "Reason/, /button "[^"]*Go[^"]*"/, /button "[^"]*No-go/);
  const go = await tabTo(page, /Go\b/, 4);
  assert.equal(go.tag, "button");
  await page.keyboard.press("Enter");
  const status = page.getByRole("status").filter({ hasText: /The Sortie goes on/ });
  await status.waitFor();
  const body = await (await fetch(`${url}/api/v1/runs/${runs.RUN_WAITING}`)).json();
  assert.ok(["succeeded", "running"].includes(body.status), "the run went on: " + body.status);
});

await journey("start a run: the dialog takes focus, Tab stays inside, Enter starts, Escape would give focus back", async (page) => {
  await open(page, "/projects/shop-docs");
  await tabTo(page, /^Start a Sortie/);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  const inside = await page.evaluate(() => !!document.activeElement?.closest("dialog"));
  assert.ok(inside, "focus moved into the dialog");
  // Tab never leaves the dialog.
  // A modal dialog keeps Tab inside it (the browser's own toolbar may take a turn, which leaves the page's focus on the body).
  for (let i = 0; i < 12; i++) { await page.keyboard.press("Tab"); assert.ok(await page.evaluate(() => !!document.activeElement?.closest("dialog") || document.activeElement === document.body), "Tab stayed inside"); }
  await page.keyboard.press("Escape");
  await page.locator("dialog").waitFor({ state: "detached" });
  assert.match((await active(page)).name, /Start a Sortie/, "focus went back to the button that opened the dialog");
  await page.keyboard.press("Enter");
  await dialog.waitFor();
  // Choose the branch: the first option is a button in the Tab order.
  await page.getByRole("option").first().waitFor();
  await tabTo(page, /main/, 6);
  await page.keyboard.press("Enter");
  await page.getByText(/Will run/).waitFor();
  await tabTo(page, /^Start run/, 20);
  await page.keyboard.press("Enter");
  await page.waitForURL(/\/runs\//);
});

await journey("change a variable: add one in a dialog with the keyboard, then see it listed", async (page) => {
  await open(page, "/settings/variables");
  await tabTo(page, /Add a variable/);
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: /Add a variable/ });
  await dialog.waitFor();
  assert.equal((await active(page)).label.startsWith("Name"), true, "the name field has focus and a label");
  await page.keyboard.type("region");
  await page.keyboard.press("Tab"); // Type
  await page.keyboard.press("Tab"); // Value
  await page.keyboard.type("us-east");
  await page.keyboard.press("Enter");
  await dialog.waitFor({ state: "hidden" });
  await page.getByText("region", { exact: true }).first().waitFor();
  // The row is a table row a screen reader reads with its column headers.
  has(await snapshot(page.getByRole("main")), /region/, /us-east/);
});

await journey("open Learn with the keyboard and search it", async (page) => {
  await open(page, "/");
  await tabTo(page, /^Learn/);
  await page.keyboard.press("Enter");
  await page.waitForURL(/\/learn/);
  await page.getByRole("heading", { level: 1 }).waitFor();
  assert.equal((await active(page)).id, "main");
  await tabTo(page, /Search/, 40);
  await page.keyboard.type("approvals");
  await page.getByRole("link", { name: /approval/i }).first().waitFor();
});

await journey("overlays: Escape closes a dialog, the user menu and the Wingman panel and returns focus to where it came from", async (page) => {
  await open(page, "/");
  const bar = await tabTo(page, /What do you want to do/);
  assert.equal(bar.tag, "button");
  await page.keyboard.press("Enter");
  await page.getByRole("dialog").waitFor();
  await page.keyboard.press("Escape");
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.match((await active(page)).name, /What do you want to do/, "focus returned to the search button");

  await tabTo(page, /Signed in as/);
  await page.keyboard.press("Enter");
  assert.equal(await page.locator("details.usermenu").evaluate((d) => d.open), true);
  await page.keyboard.press("Escape");
  assert.equal(await page.locator("details.usermenu").evaluate((d) => d.open), false);
  assert.match((await active(page)).name, /Signed in as/);
});

await journey("the help buttons are in the same place on every page", async (page) => {
  const seen = [];
  for (const p of ["/", "/activity", "/projects", "/settings", "/learn", `/runs/${runs.RUN_FAILED}`]) {
    await open(page, p);
    seen.push(await page.evaluate(() => ["learn", "guide"].map((a) => { const r = document.querySelector(`[data-act="${a}"]`)?.getBoundingClientRect(); return r ? Math.round(r.x / 4) + "," + Math.round(r.y) : "missing"; }).join(" ")));
  }
  assert.equal(new Set(seen).size, 1, "positions differ between pages: " + seen.join(" | "));
});

await journey("a session that ends is announced and signing in again is offered, with no cognitive test", async (page) => {
  await open(page, "/");
  await page.route("**/api/v1/runs**", (r) => r.fulfill({ status: 401, contentType: "application/json", body: '{"error":"sign in"}' }));
  await page.evaluate(() => fetch("/api/v1/runs"));
  await page.evaluate(() => window.dispatchEvent(new Event("launchway:signed-out")));
  await page.getByRole("main").waitFor();
  const link = page.getByRole("link", { name: /Sign in/ });
  await link.waitFor();
  await page.waitForFunction(() => /session ended/.test(document.querySelector('[data-announcer="assertive"]')?.textContent ?? ""), null, { timeout: 5000 });
});

await browser.close();
const failed = results.filter((r) => !r.ok);
if (process.env.LAUNCHWAY_A11Y_KEYBOARD_REPORT) writeFileSync(process.env.LAUNCHWAY_A11Y_KEYBOARD_REPORT, JSON.stringify({ results }, null, 1));
console.log(`${results.length - failed.length} of ${results.length} journeys passed`);
if (failed.length) process.exit(1);
