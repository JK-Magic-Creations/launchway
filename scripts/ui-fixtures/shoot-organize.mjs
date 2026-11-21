// Screenshots of the organizing pages against a server seeded by seed-organize.sh, light and dark, at 1440 and 390 wide.
// Run: LAUNCHWAY_URL=http://127.0.0.1:PORT CHROME=/path/to/chrome PLAYWRIGHT_CORE=/path/to/playwright-core/index.mjs SHOTS_DIR=out node scripts/ui-fixtures/shoot-organize.mjs
const { chromium } = await import(process.env.PLAYWRIGHT_CORE ?? "playwright-core");
const BASE = process.env.LAUNCHWAY_URL;
const OUT = (process.env.SHOTS_DIR ?? "shots/organize") + "/";
const browser = await chromium.launch({ executablePath: process.env.CHROME, args: ["--no-sandbox"] });
const views = [
  ["home", "/", async () => {}],
  ["pipelines-cards", "/pipelines", async () => {}],
  ["pipelines-table", "/pipelines?view=table&node=wing/unit:payments", async () => {}],
  ["pipelines-filtered", "/pipelines?q=led&result=bad", async () => {}],
  ["repositories", "/repositories", async () => {}],
  ["finder", "/", async (page) => { await page.keyboard.press("/"); await page.keyboard.type("ledg", { delay: 40 }); await page.waitForTimeout(700); }],
];
for (const scheme of ["light", "dark"]) {
  for (const [w, h] of [[1440, 900], [390, 844]]) {
    for (const [name, path, act] of views) {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
      const page = await ctx.newPage();
      await page.goto(BASE + path, { waitUntil: "networkidle" }).catch(() => {});
      await page.waitForTimeout(1500);
      await act(page);
      await page.screenshot({ path: `${OUT}${name}-${scheme}-${w}.png`, fullPage: name === "home" && w === 1440 });
      await ctx.close();
    }
  }
}
await browser.close();
