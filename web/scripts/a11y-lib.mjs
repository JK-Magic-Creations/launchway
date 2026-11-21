// Shared by the browser accessibility scan (a11y-browser.mjs) and the keyboard journeys (a11y-keyboard.mjs).
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { homedir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

export const server = () => {
  const url = process.env.LAUNCHWAY_A11Y_URL;
  if (!url) throw new Error("LAUNCHWAY_A11Y_URL is not set: run this through scripts/a11y-server.sh (make web-a11y does)");
  const runs = Object.fromEntries(readFileSync(process.env.LAUNCHWAY_A11Y_RUNS, "utf8").trim().split("\n").map((l) => l.split("=")));
  return { url, runs };
};

/** The newest Chromium Playwright installed, or LAUNCHWAY_A11Y_CHROME. */
export function chromePath() {
  if (process.env.LAUNCHWAY_A11Y_CHROME) return process.env.LAUNCHWAY_A11Y_CHROME;
  const base = join(homedir(), ".cache", "ms-playwright");
  if (!existsSync(base)) return undefined;
  const dirs = readdirSync(base).filter((d) => /^chromium-\d+$/.test(d)).sort((a, b) => Number(a.split("-")[1]) - Number(b.split("-")[1]));
  for (const d of dirs.reverse()) {
    for (const sub of ["chrome-linux64", "chrome-linux"]) {
      const p = join(base, d, sub, "chrome");
      if (existsSync(p)) return p;
    }
  }
  return undefined;
}

export async function launch() {
  const { chromium } = await import("playwright-core");
  return chromium.launch({ executablePath: chromePath() });
}

export const axeSource = () => readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");
export const AXE_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"];
export const dir = here;
