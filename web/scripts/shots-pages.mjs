// Screenshots of the main pages for looking at, through scripts/a11y-server.sh:
//   LAUNCHWAY_SHOTS_OUT=dir scripts/a11y-server.sh node web/scripts/shots-pages.mjs [prefix]
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { launch, server } from "./a11y-lib.mjs";

const { url, runs } = server();
const out = process.env.LAUNCHWAY_SHOTS_OUT;
if (!out) throw new Error("set LAUNCHWAY_SHOTS_OUT");
mkdirSync(out, { recursive: true });
const prefix = process.argv[2] ? process.argv[2] + "-" : "";
const pages = [["home", "/"], ["failed-run", `/runs/${runs.RUN_FAILED}`], ["decisions", "/activity/approvals"], ["deployments", "/deployments"], ["tokens", "/settings/access/keys"], ["hangar", "/library/starters"], ["words", "/settings/words"]];
const browser = await launch();
for (const [w, h] of [[1440, 900], [390, 844]]) for (const scheme of ["light", "dark"]) for (const [name, path] of pages) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.goto(url + path);
  await page.getByRole("main").waitFor();
  await page.waitForFunction(() => !document.querySelector(".loading"));
  await page.waitForTimeout(700);
  await page.screenshot({ path: join(out, `${prefix}${name}-${w}-${scheme}.png`) });
  await ctx.close();
}
await browser.close();
