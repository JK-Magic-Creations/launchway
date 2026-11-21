// Screenshots of the code pages, for looking at: LAUNCHWAY_SHOTS_URL=http://… LAUNCHWAY_SHOTS_OUT=dir node web/scripts/shots-repo.mjs [prefix]
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { launch } from "./a11y-lib.mjs";

const url = process.env.LAUNCHWAY_SHOTS_URL, out = process.env.LAUNCHWAY_SHOTS_OUT;
if (!url || !out) throw new Error("set LAUNCHWAY_SHOTS_URL and LAUNCHWAY_SHOTS_OUT");
mkdirSync(out, { recursive: true });
const prefix = process.argv[2] ? process.argv[2] + "-" : "";
const R = "/repositories/launchway/shop";
const pages = [["tree", `${R}/code`], ["file", `${R}/code?path=internal%2Fprice%2Fprice.go`], ["blame", `${R}/blame?ref=main&path=internal%2Fprice%2Fprice.go`], ["commits", `${R}/commits`], ["branches", `${R}/branches`], ["compare", `${R}/compare?head=fix-rounding&base=main`]];
const browser = await launch();
for (const [w, h] of [[1440, 900], [390, 844]]) for (const scheme of ["light", "dark"]) for (const [name, path] of pages) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.goto(url + path);
  await page.getByRole("main").waitFor();
  await page.waitForFunction(() => !document.querySelector(".loading"));
  await page.waitForTimeout(700);
  await page.screenshot({ path: join(out, `${prefix}${name}-${w}-${scheme}.png`), fullPage: false });
  await ctx.close();
}
await browser.close();
