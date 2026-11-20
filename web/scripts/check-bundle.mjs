// Fails when the built bundle is bigger than the budget in bundle-budget.json.
//   node scripts/check-bundle.mjs <dist dir>           check
//   node scripts/check-bundle.mjs <dist dir> --update  lower the budget (never raises it)
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [dir, flag] = process.argv.slice(2);
if (!dir) { console.error("usage: check-bundle.mjs <dist dir> [--update]"); process.exit(2); }

function walk(d) {
  return readdirSync(d, { withFileTypes: true }).flatMap((e) => e.isDirectory() ? walk(join(d, e.name)) : [join(d, e.name)]);
}
const size = { js: 0, css: 0 };
for (const f of walk(dir)) {
  if (f.endsWith(".js")) size.js += statSync(f).size;
  else if (f.endsWith(".css")) size.css += statSync(f).size;
}
const budgetPath = new URL("../bundle-budget.json", import.meta.url);
let budget;
try { budget = JSON.parse(readFileSync(budgetPath, "utf8")); } catch { budget = null; }

if (flag === "--update") {
  const next = { comment: "Bundle size budget in bytes (raw). Lower it when the bundle shrinks: npm run size:update. Raising it needs a reason in the pull request.", js_bytes: size.js, css_bytes: size.css };
  if (budget && (next.js_bytes > budget.js_bytes || next.css_bytes > budget.css_bytes)) {
    console.error("refusing to raise the budget; shrink the bundle or raise it by hand with a reason");
    process.exit(1);
  }
  writeFileSync(budgetPath, JSON.stringify(next, null, 2) + "\n");
  console.log(`budget written: js ${size.js} B, css ${size.css} B`);
  process.exit(0);
}
if (!budget) { console.error("bundle-budget.json is missing; run npm run size:update"); process.exit(1); }
let bad = false;
for (const k of ["js", "css"]) {
  const limit = budget[`${k}_bytes`];
  const msg = `${k}: ${size[k]} B of ${limit} B`;
  if (size[k] > limit) { console.error(`bundle over budget, ${msg}. Split the code (React.lazy per route) or justify raising the budget.`); bad = true; }
  else console.log(`bundle ok, ${msg}`);
}
process.exit(bad ? 1 : 0);
