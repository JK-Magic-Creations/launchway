// The real-browser accessibility scan (make web-a11y): axe-core in Chromium over the main pages, at 1440 and 390
// wide, in light and dark, with color contrast and target size (which jsdom cannot compute), plus reflow at 320 px,
// the visible focus ring, and text-spacing overrides. Run through scripts/a11y-server.sh, which starts a server
// with demo data. Findings not in a11y-browser-baseline.json fail; baseline lines that no longer occur fail too, so
// the baseline only shrinks.
//
//   LAUNCHWAY_A11Y_SHOTS=dir   also writes a screenshot of every page and mode
//   LAUNCHWAY_A11Y_REPORT=file writes the results as JSON (the conformance statement is built from it)
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import { AXE_TAGS, axeSource, dir, launch, server } from "./a11y-lib.mjs";

const { url, runs } = server();
const baselinePath = join(dir, "..", "a11y-browser-baseline.json");
// LAUNCHWAY_A11Y_NO_BASELINE=1 counts every finding, for measuring a build from before the baseline existed.
const baseline = new Set(process.env.LAUNCHWAY_A11Y_NO_BASELINE ? [] : JSON.parse(readFileSync(baselinePath, "utf8")).findings);
const seen = new Set();

const PAGES = [
  ["Home", "/"], ["Runs", "/activity"], ["A failed run", `/runs/${runs.RUN_FAILED}`], ["A finished run", `/runs/${runs.RUN_OK}`],
  ["A run waiting", `/runs/${runs.RUN_WAITING}`], ["Decisions", "/activity/approvals"], ["Pipelines", "/pipelines"], ["Code", "/projects"],
  ["A repository", "/projects/shop-api"], ["A pipeline", "/projects/shop-api/pipeline"], ["Deployments", "/deployments"],
  ["Applications", "/deployments/applications"], ["Environments", "/deployments/environments"], ["Images", "/depot"],
  ["Library", "/library"], ["Settings", "/settings"], ["Runners", "/settings/runners"], ["Connections to Git hosts", "/settings/connections"], ["Code host of a project", "/projects/shop-api/outside"], ["People and access", "/settings/access"],
  ["Variables", "/settings/variables"], ["Rules", "/settings/rules"], ["Learn", "/learn"], ["A Learn page", "/learn/concepts/launch-line"],
  ["Accessibility guide", "/learn/guide/accessibility"], ["Conformance statement", "/learn/reference/accessibility-conformance"],
  ["Configuration", "/config"], ["First-run wizard", "/config/wizard/connect-repository"], ["Design kit", "/kit"],
  ["Repository code", "/repositories/launchway/shop/code"], ["A file", "/repositories/launchway/shop/code?path=internal%2Fprice%2Fprice.go"],
  ["Blame", "/repositories/launchway/shop/blame?ref=main&path=internal%2Fprice%2Fprice.go"], ["Commits", "/repositories/launchway/shop/commits"],
  ["Branches", "/repositories/launchway/shop/branches"], ["Compare", "/repositories/launchway/shop/compare?head=fix&base=main"],
  ["Events", "/activity/events"], ["Releases", "/deployments/releases"], ["A published release", "/deployments/releases/shop-docs/1.0.0"],
  ["The page a QR code opens", "/deployments/releases/shop-docs/1.0.0/get/shop-docs.apk"], ["Formations", "/deployments/releases/formations"],
  ["The software graph", "/graph"], ["A repository's graph", "/repositories/launchway/shop/graph"], ["Pull requests", "/repositories/launchway/shop/pulls"],
  ["Standing orders of a project", "/projects/shop-api/standards"], ["Standing orders settings", "/settings/standards"],
  ["Build caches", "/projects/shop-api/caches"], ["Configuration of a project", "/projects/shop-api/configuration"],
  ["Pipeline templates", "/library/starters"], ["Depot", "/depot/store"], ["Switches", "/projects/shop-api/toggles"],
  ["Tests of a project", "/projects/shop-api/tests"],
  ["API tokens", "/settings/access/keys"], ["Repository settings", "/repositories/launchway/shop/settings"],
  ["Who can reach a repository", "/repositories/launchway/shop/settings?tab=reach"],
  ["Squadrons", "/units"], ["A Squadron", "/units/shop"], ["An empty Squadron", "/units/archive"], ["Add a Squadron", "/units/new"],
  ["Add a Flight", "/units/shop/projects/new"], ["Choose a Squadron", "/projects/new"], ["Organize into Squadrons", "/units/organize"],
  ["Sign-in", "/signin", { signedOut: true }],
];
const VIEWS = [["1440", { width: 1440, height: 900 }], ["390", { width: 390, height: 844 }]];
const THEMES = ["light", "dark"];

const axe = axeSource();
const browser = await launch();
const results = [];
const failures = [];
const notes = [];
const shots = process.env.LAUNCHWAY_A11Y_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });

const settle = async (page) => {
  await page.waitForSelector("main, .signin, h1", { timeout: 20000 });
  await page.waitForFunction(() => !document.querySelector(".loading"), null, { timeout: 20000 }).catch(() => {});
  await page.waitForTimeout(500);
};

for (const [name, path, opt = {}] of PAGES) {
  for (const [vname, viewport] of VIEWS) {
    for (const theme of THEMES) {
      const ctx = await browser.newContext({ viewport, colorScheme: theme, reducedMotion: "reduce", bypassCSP: true });
      const page = await ctx.newPage();
      if (opt.signedOut) await page.route("**/api/v1/me", (r) => r.fulfill({ status: 401, contentType: "application/json", body: '{"error":"sign in"}' }));
      try {
        await page.goto(url + path);
        await settle(page);
        await page.addScriptTag({ content: axe });
        const res = await page.evaluate(async (tags) => {
          // eslint-disable-next-line no-undef
          const r = await axe.run(document, { runOnly: { type: "tag", values: tags }, resultTypes: ["violations"] });
          return r.violations.map((v) => ({ rule: v.id, impact: v.impact, help: v.help, nodes: v.nodes.map((n) => ({ target: n.target.join(" "), html: n.html.slice(0, 140), why: (n.any[0] || n.all[0] || n.none[0] || {}).message })) }));
        }, AXE_TAGS);
        for (const v of res) {
          const key = `${path.replace(runs.RUN_FAILED, ":failed").replace(runs.RUN_OK, ":ok").replace(runs.RUN_WAITING, ":waiting")}|${v.rule}`;
          results.push({ page: name, path, view: vname, theme, baselined: baseline.has(key), ...v });
          if (baseline.has(key)) { seen.add(key); continue; }
          failures.push(`${name} ${vname} ${theme}: ${v.rule} (${v.impact}) ${v.help}\n      ${v.nodes.slice(0, 3).map((n) => `${n.target}: ${n.why ?? ""}`).join("\n      ")}`);
        }
        if (shots) await page.screenshot({ path: join(shots, `${name.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-${vname}-${theme}.png`) });
        // Reflow (1.4.10): no horizontal scrolling of the page at 320 px wide.
        if (vname === "390" && theme === "light") {
          await page.setViewportSize({ width: 320, height: 800 });
          await page.waitForTimeout(250);
          const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          if (over > 1) failures.push(`${name} 320: the page scrolls sideways by ${over}px (reflow, 1.4.10)`);
          results.push({ page: name, path, view: "320", theme, rule: "reflow", overflow: over });
        }
        // Text spacing (1.4.12): with the spacing the criterion names, nothing is cut off.
        if (vname === "1440" && theme === "light") {
          await page.addStyleTag({ content: "*{line-height:1.5 !important;letter-spacing:.12em !important;word-spacing:.16em !important} p{margin-bottom:2em !important}" });
          await page.waitForTimeout(250);
          const cut = await page.evaluate(() => [...document.querySelectorAll("main p, main h1, main h2, main li, main label, main button, main a")]
            .filter((e) => { const s = getComputedStyle(e); return (s.overflow === "hidden" || s.overflowY === "hidden") && s.textOverflow !== "ellipsis" && e.scrollHeight > e.clientHeight + 3 && e.clientHeight > 0; })
            .map((e) => e.tagName.toLowerCase() + "." + e.className).slice(0, 5));
          results.push({ page: name, path, view: vname, theme, rule: "text-spacing", cut });
          if (cut.length) failures.push(`${name}: text is cut off with the 1.4.12 text spacing: ${cut.join(", ")}`);
        }
      } catch (e) {
        failures.push(`${name} ${vname} ${theme}: ${e.message.split("\n")[0]}`);
      } finally {
        await ctx.close();
      }
    }
  }
}

// Focus ring (2.4.7, 2.4.11, 1.4.11): tabbing onto each of the first controls draws a 2 px ring that differs from its surroundings.
for (const theme of THEMES) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: theme, bypassCSP: true });
  const page = await ctx.newPage();
  await page.goto(url + "/");
  await settle(page);
  let bad = 0; let count = 0;
  for (let i = 0; i < 25; i++) {
    await page.keyboard.press("Tab");
    const r = await page.evaluate(() => {
      const e = document.activeElement;
      if (!e || e === document.body) return null;
      const s = getComputedStyle(e);
      const ring = s.outlineStyle !== "none" && parseFloat(s.outlineWidth) >= 2;
      const shadow = s.boxShadow !== "none";
      return { ok: ring || shadow, el: e.tagName.toLowerCase() + (e.getAttribute("aria-label") ? `[${e.getAttribute("aria-label")}]` : "") };
    });
    if (!r) continue;
    count++;
    if (!r.ok) { bad++; failures.push(`Home ${theme}: ${r.el} shows no focus ring when tabbed to`); }
  }
  results.push({ page: "Home", rule: "focus-visible", theme, checked: count, without: bad });
  await ctx.close();
}
await browser.close();

for (const k of baseline) if (!seen.has(k) && !process.env.LAUNCHWAY_A11Y_NO_BASELINE) failures.push(`baseline line no longer occurs, delete it from web/a11y-browser-baseline.json: ${k}`);
if (process.env.LAUNCHWAY_A11Y_REPORT) writeFileSync(process.env.LAUNCHWAY_A11Y_REPORT, JSON.stringify({ pages: PAGES.map((p) => p[0]), views: VIEWS.map((v) => v[0]), themes: THEMES, results, failures }, null, 1));
for (const n of notes) console.log(n);
console.log(`${PAGES.length} pages x ${VIEWS.length} widths x ${THEMES.length} themes scanned: ${results.filter((r) => r.rule !== "reflow" && r.rule !== "text-spacing" && r.rule !== "focus-visible").length} axe findings, ${failures.length} failures`);
if (failures.length) { console.error("\n" + failures.join("\n")); process.exit(1); }
