// Screenshots of the interface in the display modes people rely on, for looking at (and for the documentation of a
// change): high contrast (forced colors), more contrast, reduced motion, text spacing, 200 percent zoom and 320 pixels.
// Run through scripts/a11y-server.sh:  LAUNCHWAY_A11Y_SHOTS=dir scripts/a11y-server.sh node web/scripts/a11y-shots.mjs [prefix]
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { launch, server } from "./a11y-lib.mjs";

const { url, runs } = server();
const out = process.env.LAUNCHWAY_A11Y_SHOTS;
if (!out) throw new Error("LAUNCHWAY_A11Y_SHOTS is not set");
mkdirSync(out, { recursive: true });
const prefix = process.argv[2] ? process.argv[2] + "-" : "";
const browser = await launch();

const pages = [["home", "/"], ["failed-run", `/runs/${runs.RUN_FAILED}`], ["decisions", "/activity/approvals"], ["learn-launch-line", "/learn/concepts/launch-line"]];
const modes = {
  "forced-colors": { colorScheme: "dark", forcedColors: "active" },
  "high-contrast-light": { colorScheme: "light", contrast: "more" },
  "high-contrast-dark": { colorScheme: "dark", contrast: "more" },
  "reduced-motion-dark": { colorScheme: "dark", reducedMotion: "reduce" },
  "light": { colorScheme: "light" },
  "dark": { colorScheme: "dark" },
};
for (const [mode, opts] of Object.entries(modes)) {
  for (const [name, path] of pages) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, ...opts });
    const page = await ctx.newPage();
    await page.goto(url + path);
    await page.getByRole("main").waitFor();
    await page.waitForFunction(() => !document.querySelector(".loading"));
    await page.waitForTimeout(600);
    await page.screenshot({ path: join(out, `${prefix}${name}-${mode}.png`) });
    await ctx.close();
  }
}
// The focus ring, after tabbing to the first control of the page's content, in light and dark.
for (const scheme of ["light", "dark"]) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: scheme });
  const page = await ctx.newPage();
  await page.goto(url + "/");
  await page.getByRole("main").waitFor();
  await page.waitForTimeout(500);
  for (let i = 0; i < 8; i++) await page.keyboard.press("Tab");
  await page.screenshot({ path: join(out, `${prefix}focus-ring-${scheme}.png`), clip: { x: 0, y: 0, width: 1440, height: 260 } });
  await ctx.close();
}
// 320 wide, and the text spacing overrides, on the failed run.
{
  const ctx = await browser.newContext({ viewport: { width: 320, height: 800 }, colorScheme: "light" });
  const page = await ctx.newPage();
  await page.goto(url + `/runs/${runs.RUN_FAILED}`);
  await page.getByRole("main").waitFor();
  await page.waitForTimeout(600);
  await page.screenshot({ path: join(out, `${prefix}failed-run-320.png`), fullPage: false });
  await ctx.close();
  const c2 = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "light" });
  const p2 = await c2.newPage();
  await p2.goto(url + `/runs/${runs.RUN_FAILED}`);
  await p2.getByRole("main").waitFor();
  await p2.addStyleTag({ content: "*{line-height:1.5 !important;letter-spacing:.12em !important;word-spacing:.16em !important} p{margin-bottom:2em !important}" });
  await p2.waitForTimeout(500);
  await p2.screenshot({ path: join(out, `${prefix}failed-run-text-spacing.png`) });
  await c2.close();
}
await browser.close();
console.log("screenshots in " + out);
