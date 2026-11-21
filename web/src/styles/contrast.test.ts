import { describe, expect, it } from "vitest";
// @ts-expect-error a plain module shared with scripts/a11y-conformance.mjs
import { MIN, evaluate, readTokens } from "../../scripts/contrast-lib.mjs";

interface Row { theme: string; kind: string; fg: string; bg: string; what: string; ratio: number; min: number; ok: boolean }

// WCAG 2.2 1.4.3 (text 4.5:1) and 1.4.11 (graphics, control borders and the focus indicator 3:1), computed from
// the tokens in tokens.css for both themes. A palette change that breaks a pair fails the build.
describe("design token contrast", () => {
  const rows = evaluate() as Row[];
  for (const theme of ["light", "dark"]) {
    it(`meets the minimum for every pair in the ${theme} theme`, () => {
      const bad = rows.filter((r) => r.theme === theme && !r.ok).map((r) => `${r.fg} on ${r.bg} (${r.what}): ${r.ratio}:1, needs ${r.min}:1`);
      expect(bad).toEqual([]);
    });
  }
  it("covers text, graphics and the focus ring", () => {
    expect(new Set(rows.map((r) => r.kind))).toEqual(new Set(["text", "ui", "focus"]));
    expect(MIN.text).toBe(4.5);
    expect(rows.length).toBeGreaterThan(80);
  });
  it("defines every token a pair names, in both themes", () => {
    const t = readTokens();
    for (const r of rows) for (const k of [r.fg, r.bg]) expect(t[r.theme as "light"][k], `${k} in ${r.theme}`).toMatch(/^#[0-9a-f]{6}$/i);
  });
});
