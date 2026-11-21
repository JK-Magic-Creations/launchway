// Contrast arithmetic and the list of colour pairs the interface uses, computed from the design tokens in
// src/styles/tokens.css for both themes. Shared by the contrast test (src/styles/contrast.test.ts), which
// fails the build when a pair drops under its minimum, and by scripts/a11y-conformance.mjs, which writes the
// results into the conformance statement. WCAG 2.2: 1.4.3 text 4.5:1 (large text 3:1), 1.4.11 non-text 3:1.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

export const MIN = { text: 4.5, large: 3, ui: 3, focus: 3 };

/** The tokens of each theme, aliases (var(--x)) resolved. */
export function readTokens(cssPath = join(dirname(fileURLToPath(import.meta.url)), "../src/styles/tokens.css")) {
  const css = readFileSync(cssPath, "utf8");
  const block = (head) => {
    const i = css.indexOf(head);
    if (i < 0) throw new Error(`tokens.css has no ${head}`);
    const open = css.indexOf("{", i);
    return css.slice(open + 1, css.indexOf("}", open));
  };
  const decls = (body) => Object.fromEntries([...body.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]));
  const light = decls(block(":root {"));
  const dark = { ...light, ...decls(block(':root[data-theme="dark"] {')) };
  const resolve = (t) => {
    const out = {};
    const get = (k, depth = 0) => {
      const v = t[k];
      if (v === undefined || depth > 8) return undefined;
      const m = /^var\((--[a-z0-9-]+)\)$/.exec(v);
      return m ? get(m[1], depth + 1) : v;
    };
    for (const k of Object.keys(t)) out[k] = get(k);
    return out;
  };
  return { light: resolve(light), dark: resolve(dark) };
}

export function hex(c) {
  const m = /^#([0-9a-f]{6})$/i.exec(c ?? "");
  if (!m) throw new Error(`not a #rrggbb colour: ${c}`);
  return [0, 2, 4].map((i) => parseInt(m[1].slice(i, i + 2), 16));
}
const lin = (v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4; };
export function luminance(c) { const [r, g, b] = hex(c).map(lin); return 0.2126 * r + 0.7152 * g + 0.0722 * b; }
export function ratio(a, b) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

const SURFACES = ["--bg", "--sheet", "--sheet-2", "--sheet-3"];

/** Every pair: [kind, foreground token, background token, what it is]. */
export function pairs() {
  const out = [];
  for (const fg of ["--ink", "--ink-2", "--ink-3", "--signal-ink", "--done"]) for (const bg of SURFACES) out.push(["text", fg, bg, "text on a surface"]);
  out.push(["text", "--ink", "--signal-wash", "text on the accent wash"], ["text", "--signal-ink", "--signal-wash", "link on the accent wash"]);
  for (const t of ["ok", "bad", "warn"]) {
    out.push(["text", `--${t}-ink`, `--${t}-wash`, `${t} chip text`], ["text", `--${t}-ink`, "--sheet", `${t} text on a sheet`]);
  }
  for (const t of ["--ok", "--bad", "--warn", "--done"]) out.push(["ui", "--on-solid", t, "icon or text on a solid status fill"]);
  out.push(["text", "--on-solid", "--bad", "badge count on red"], ["text", "--ink-2", "--idle-wash", "neutral chip text"], ["text", "--on-signal", "--signal", "text on a primary button"],
    ["text", "--on-signal", "--signal-ink", "text on a pressed primary button"]);
  for (const fg of ["--log-ink", "--log-dim", "--log-bad", "--log-ok"]) out.push(["text", fg, "--log", "log text"]);
  // Graphics and controls (1.4.11): status discs, control borders, the focus ring.
  for (const t of ["--ok", "--bad", "--warn", "--idle", "--signal", "--done"]) for (const bg of ["--sheet", "--bg"]) out.push(["ui", t, bg, "status mark or accent"]);
  for (const bg of ["--sheet", "--sheet-2", "--bg"]) out.push(["ui", "--control-line", bg, "border of a control"]);
  for (const bg of SURFACES) out.push(["focus", "--signal", bg, "focus ring"]);
  out.push(["focus", "--signal", "--signal-wash", "focus ring on the accent wash"]);
  return out;
}

export function evaluate(tokens = readTokens()) {
  const rows = [];
  for (const theme of ["light", "dark"]) {
    for (const [kind, fg, bg, what] of pairs()) {
      const r = ratio(tokens[theme][fg], tokens[theme][bg]);
      const min = kind === "text" ? MIN.text : MIN[kind];
      rows.push({ theme, kind, fg, bg, what, ratio: Math.round(r * 100) / 100, min, ok: r >= min });
    }
  }
  return rows;
}
