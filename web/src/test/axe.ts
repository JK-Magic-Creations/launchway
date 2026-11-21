import axe from "axe-core";
import { expect } from "vitest";

/** The WCAG 2.2 AA rule set (and best practices), run by axe-core in jsdom. jsdom draws nothing, so the rules
 * that need layout or computed color (color-contrast, target-size, scrollable regions) cannot run here: the
 * contrast test (styles/contrast.test.ts) covers the colors, and the browser pass (make web-a11y) covers the rest. */
const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"];

/** Rules that only make sense for a whole page, not a fragment such as one component. */
const PAGE_RULES = ["region", "landmark-one-main", "page-has-heading-one", "landmark-unique", "bypass"];

export interface Found { rule: string; impact: string; help: string; nodes: string[] }

export async function scan(root: Element, opts: { page?: boolean } = {}): Promise<Found[]> {
  const rules: Record<string, { enabled: boolean }> = { "color-contrast": { enabled: false }, "target-size": { enabled: false } };
  if (!opts.page) for (const r of PAGE_RULES) rules[r] = { enabled: false };
  const res = await axe.run(root, { runOnly: { type: "tag", values: TAGS }, rules, resultTypes: ["violations"] });
  return res.violations.map((v) => ({
    rule: v.id, impact: v.impact ?? "unknown", help: v.help, nodes: v.nodes.map((n) => n.html.slice(0, 160)),
  }));
}

/** Fails the test with each violation, its rule, how bad it is and the markup. */
export async function expectAccessible(root: Element, opts: { page?: boolean; name?: string; allow?: string[] } = {}) {
  const found = (await scan(root, opts)).filter((f) => !opts.allow?.includes(f.rule));
  const text = found.map((f) => `${f.rule} (${f.impact}): ${f.help}\n    ${f.nodes.join("\n    ")}`).join("\n");
  expect(found, `${opts.name ?? "page"} has accessibility violations:\n${text}`).toEqual([]);
}
