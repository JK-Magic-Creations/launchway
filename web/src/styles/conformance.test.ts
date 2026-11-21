import { describe, expect, it } from "vitest";
import statement from "../../../docs/reference/accessibility-conformance.md?raw";
// @ts-expect-error a plain module that also runs as a script
import { build, inputs } from "../../scripts/a11y-conformance.mjs";

// The conformance statement is made from the contrast results, the last recorded browser run and the baselines. If a
// palette change moves a ratio, or a baseline line goes away, the page must be written again:
//   node web/scripts/a11y-conformance.mjs
describe("the accessibility conformance statement", () => {
  it("is current", () => {
    const want = (build(inputs()) as string).replace(/\n{3,}/g, "\n\n").trimEnd() + "\n";
    expect(statement, "run: node web/scripts/a11y-conformance.mjs").toBe(want);
  });
  it("names every criterion once and gives each an answer", () => {
    const text = statement;
    const rows = [...text.matchAll(/^\| (\d\.\d+\.\d+) [^|]+ \| (A|AA) \| (Supports|Partially supports|Does not support|Not applicable) \|/gm)];
    const ids = rows.map((r) => r[1]);
    expect(new Set(ids).size).toBe(ids.length);
    // WCAG 2.2 has 55 criteria at levels A and AA (4.1.1 Parsing is removed, six were added).
    expect(ids.length).toBe(55);
  });
});
