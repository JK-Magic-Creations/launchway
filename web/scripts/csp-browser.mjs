// The Content-Security-Policy test in a real browser (make web-csp): every main page loads with the
// policy the server sends and the browser reports no violation and no script error, and an injected
// inline script does not run. Run through scripts/a11y-server.sh, which starts a server with demo data.
import { launch, server } from "./a11y-lib.mjs";

const { url, runs } = server();
const PAGES = ["/", "/activity", `/runs/${runs.RUN_FAILED}`, `/runs/${runs.RUN_OK}`, `/runs/${runs.RUN_WAITING}`, "/activity/approvals", "/pipelines",
  "/projects", "/projects/shop-api", "/projects/shop-api/pipeline", "/deployments", "/images", "/library", "/settings", "/settings/runners",
  "/settings/access", "/settings/variables", "/learn", "/learn/concepts/launch-line", "/config", "/kit", "/signin"];

const browser = await launch();
const problems = [];
let csp = "";
for (const path of PAGES) {
  const page = await (await browser.newContext()).newPage();
  page.on("console", (m) => { if (m.type() === "error" || /content security policy|refused to/i.test(m.text())) problems.push(`${path}: console: ${m.text().slice(0, 200)}`); });
  page.on("pageerror", (e) => problems.push(`${path}: page error: ${String(e).slice(0, 200)}`));
  const resp = await page.goto(url + path);
  csp = resp.headers()["content-security-policy"] ?? "";
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.close();
}
// The policy must also stop an inline script from running.
const page = await (await browser.newContext()).newPage();
await page.goto(url + "/");
const ran = await page.evaluate(() => {
  window.__inlineRan = false;
  const s = document.createElement("script");
  s.textContent = "window.__inlineRan = true";
  document.head.appendChild(s);
  return window.__inlineRan;
});
if (ran) problems.push("an inline script ran: the policy does not stop it");
await browser.close();
if (!csp.includes("script-src 'self'")) problems.push(`the server sent no script-src 'self': ${csp}`);
if (problems.length) {
  console.error(`web-csp: ${problems.length} problem(s)\n  ${[...new Set(problems)].join("\n  ")}`);
  process.exit(1);
}
console.log(`web-csp: ${PAGES.length} pages load under the policy without a violation; inline scripts are blocked`);
