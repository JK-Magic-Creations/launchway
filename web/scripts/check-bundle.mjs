// Fails when the built bundle is bigger than the budget in bundle-budget.json. Two measures:
//   initial  the scripts and styles a first visit downloads before anything is drawn: what index.html names,
//            and the chunks those import statically. This is what a person waits for.
//   total    every script and style file in the build, including each lazy page. Lazy loading moves code out of
//            the initial load but does not make the sum smaller, so total has its own, generous cap that
//            still catches runaway growth.
//   node scripts/check-bundle.mjs <dist dir>           check
//   node scripts/check-bundle.mjs <dist dir> --update  lower the budget (never raises it)
import { readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

const [dir, flag] = process.argv.slice(2);
if (!dir) { console.error("usage: check-bundle.mjs <dist dir> [--update]"); process.exit(2); }

function walk(d) {
  return readdirSync(d, { withFileTypes: true }).flatMap((e) => e.isDirectory() ? walk(join(d, e.name)) : [join(d, e.name)]);
}
const kind = (f) => f.endsWith(".js") ? "js" : f.endsWith(".css") ? "css" : "";
const total = { js: 0, css: 0 };
for (const f of walk(dir)) { const k = kind(f); if (k) total[k] += statSync(f).size; }

// The initial load: index.html's scripts, preloads and stylesheets, then static imports of each script. A dynamic
// import("./X.js") is lazy and is not followed; a static `from"./X.js"` or `import"./X.js"` is.
const html = readFileSync(join(dir, "index.html"), "utf8");
const queue = [...html.matchAll(/(?:src|href)="(\/[^"]+\.(?:js|css))"/g)].map((m) => join(dir, m[1]));
const initialFiles = new Set();
while (queue.length) {
  const f = queue.pop();
  if (initialFiles.has(f)) continue;
  initialFiles.add(f);
  if (kind(f) !== "js") continue;
  const src = readFileSync(f, "utf8").replace(/import\(\s*["'`][^"'`]*["'`]\s*\)/g, "");
  for (const m of src.matchAll(/(?:from|import)\s*["'](\.\/[^"']+\.js)["']/g)) queue.push(resolve(dirname(f), m[1]));
}
const initial = { js: 0, css: 0 };
for (const f of initialFiles) { const k = kind(f); if (k) initial[k] += statSync(f).size; }

const budgetPath = new URL("../bundle-budget.json", import.meta.url);
let budget;
try { budget = JSON.parse(readFileSync(budgetPath, "utf8")); } catch { budget = null; }
const keys = { initial_js_bytes: initial.js, initial_css_bytes: initial.css, total_js_bytes: total.js, total_css_bytes: total.css };

if (flag === "--update") {
  // Lowers each limit to today's size plus 1% headroom (rounded up to 1,000); a limit is never raised, so when the
  // build has grown nothing is written and the output says so for each limit.
  const next = { comment: budget?.comment ?? "" };
  let lowered = 0;
  for (const [k, v] of Object.entries(keys)) {
    const old = budget?.[k];
    const want = Math.ceil(v * 1.01 / 1000) * 1000;
    next[k] = Math.min(old ?? Infinity, want);
    const note = old === undefined ? "set" : next[k] < old ? "lowered" : want > old ? "kept (today's size plus headroom would be above it; a limit is never raised here)" : "kept";
    if (next[k] !== old) lowered++;
    console.log(`${k}: measured ${v} B, limit ${old ?? "none"} -> ${next[k]} B, ${note}`);
  }
  if (lowered === 0) { console.log("bundle-budget.json is unchanged: no limit could be lowered."); process.exit(0); }
  writeFileSync(budgetPath, JSON.stringify(next, null, 2) + "\n");
  console.log(`bundle-budget.json written (${lowered} limit${lowered === 1 ? "" : "s"} changed).`);
  process.exit(0);
}
if (!budget) { console.error("bundle-budget.json is missing; run npm run size:update"); process.exit(1); }
let bad = false;
for (const [k, v] of Object.entries(keys)) {
  const limit = budget[k];
  const msg = `${k.replace(/_bytes$/, "").replace("_", " ")}: ${v} B of ${limit} B`;
  if (!(v <= limit)) { console.error(`bundle over budget, ${msg}. ${k.startsWith("initial") ? "Split the code (React.lazy per route) so the first load shrinks." : "The whole build grew a lot: is something in it needed?"}`); bad = true; }
  else console.log(`bundle ok, ${msg}`);
}
process.exit(bad ? 1 : 0);
