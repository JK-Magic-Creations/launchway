// Writes docs/reference/accessibility-conformance.md, the conformance statement in the VPAT table form (criterion,
// level, conformance, remarks), from what the tests measure:
//   - the contrast results, computed from the design tokens for both themes (always current);
//   - the last browser scan and keyboard run, kept in web/a11y-last-run.json (written by make web-a11y);
//   - the baselines (the findings that are still open).
// The rest of each remark is a judgment, written here beside the criterion, and says what is tested and what is not.
//
//   node web/scripts/a11y-conformance.mjs            write the page
//   node web/scripts/a11y-conformance.mjs --check    fail if the page is not current
//   node web/scripts/a11y-conformance.mjs --snapshot REPORT KEYBOARD   record a run, then write the page
import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { evaluate, readTokens } from "./contrast-lib.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");
const pagePath = join(root, "docs", "reference", "accessibility-conformance.md");
const snapPath = join(here, "..", "a11y-last-run.json");

const SUP = "Supports", PART = "Partially supports", NO = "Does not support", NA = "Not applicable";

/** The text of the page, from the tokens, the last run and the baselines. */
export function build({ snapshot, baselines, rows }) {
  const text = Math.min(...rows.filter((r) => r.kind === "text").map((r) => r.ratio));
  const ui = Math.min(...rows.filter((r) => r.kind === "ui" || r.kind === "focus").map((r) => r.ratio));
  const worst = (theme, kind) => Math.min(...rows.filter((r) => r.theme === theme && (kind === "text" ? r.kind === "text" : r.kind !== "text")).map((r) => r.ratio));
  const pairs = rows.length / 2;
  const s = snapshot;
  const scan = s ? `the browser scan of ${s.pages} pages at ${s.views.join(" and ")} wide in light and dark found ${s.axeFindings} violations that are not in the baseline` : "the browser scan has not been recorded yet";
  const openRules = [...new Set([...baselines.jsdom, ...baselines.browser])];
  const open = openRules.length;
  const C = [
    ["Perceivable"],
    ["1.1.1", "Non-text Content", "A", PART, `Icons are hidden from assistive technology and every status mark has a name (tested). The launch line is also a list, and Learn diagrams come with their text. The trend charts have a text summary but not each a table of the same numbers (gap).`],
    ["1.2.1", "Audio-only and Video-only (Prerecorded)", "A", NA, "The product has no audio or video. The documentation site has one silent animated tour of starting a run; the same steps are in the text beside it."],
    ["1.2.2", "Captions (Prerecorded)", "A", NA, "No audio."],
    ["1.2.3", "Audio Description or Media Alternative (Prerecorded)", "A", NA, "No audio or video in the product."],
    ["1.2.4", "Captions (Live)", "AA", NA, "No live audio."],
    ["1.2.5", "Audio Description (Prerecorded)", "AA", NA, "No video with sound."],
    ["1.3.1", "Info and Relationships", "A", open > 0 ? PART : SUP, `Headings, lists, tables, landmarks, labels and ARIA are checked by axe-core on every kit part and routed page (\`make check\`) and in a browser (\`make web-a11y\`). Open: ${openRules.join(", ") || "none"}.`],
    ["1.3.2", "Meaningful Sequence", "A", SUP, "The page is built in reading order and the Tab order follows it (keyboard journeys)."],
    ["1.3.3", "Sensory Characteristics", "A", SUP, "Instructions name things by their words, not by color, shape or place."],
    ["1.3.4", "Orientation", "AA", SUP, "No orientation is locked; the pages are scanned at 1440 and 390 pixels wide."],
    ["1.3.5", "Identify Input Purpose", "AA", PART, "Launchway asks for few personal details, and its fields do not yet carry the autocomplete attribute (gap)."],
    ["1.4.1", "Use of Color", "A", SUP, "A status is a shape, a color and a word (test: `src/a11y/kit.a11y.test.tsx`). Links in sentences are underlined, and axe-core's link-in-text-block rule passes in the browser scan."],
    ["1.4.2", "Audio Control", "A", NA, "No audio."],
    ["1.4.3", "Contrast (Minimum)", "AA", SUP, `Computed from the design tokens for ${pairs} color pairs in each theme: the lowest text ratio is ${worst("light", "text")}:1 in light and ${worst("dark", "text")}:1 in dark (4.5:1 needed). Also scanned in the browser, where ${scan}.`],
    ["1.4.4", "Resize Text", "AA", SUP, "Layout holds at 400 percent zoom, which is the 320 pixel reflow test below."],
    ["1.4.5", "Images of Text", "AA", SUP, "Text is text. The logo is a drawing."],
    ["1.4.10", "Reflow", "AA", SUP, `The browser scan loads every page at 320 pixels wide and fails if the page scrolls sideways${s ? ` (${s.reflowPages} pages checked)` : ""}. Tables, code and drawings scroll inside their own region, which can be focused.`],
    ["1.4.11", "Non-text Contrast", "AA", SUP, `Status marks, control borders (\`--control-line\`) and the focus ring are computed from the tokens: the lowest is ${ui}:1 (3:1 needed), both themes.`],
    ["1.4.12", "Text Spacing", "AA", PART, `The browser scan applies the spacing the criterion names and fails if text is cut off. Some lists shorten a long name with an ellipsis (full text in the tooltip and for screen readers).`],
    ["1.4.13", "Content on Hover or Focus", "AA", PART, "The help that appears on an area in the top bar is hoverable and appears on focus, but cannot be dismissed with Escape (gap). The same text is always available to screen readers."],
    ["Operable"],
    ["2.1.1", "Keyboard", "A", SUP, `Keyboard-only journeys: ${s ? `${s.journeysPassed} of ${s.journeysTotal} pass` : "not recorded yet"} (skip link, search and read a failed run, decide an approval, start a run, change a variable, Learn, overlays, a session that ends).`],
    ["2.1.2", "No Keyboard Trap", "A", SUP, "A dialog keeps Tab inside on purpose and always closes with Escape (journey: start a run, overlays)."],
    ["2.1.4", "Character Key Shortcuts", "A", SUP, "The only single-key shortcut, / for search, is switched off under the user menu."],
    ["2.2.1", "Timing Adjustable", "A", PART, "A session ends after idle time (use keeps it going) and after its lifetime. The end is announced; there is no warning before it and no button to extend it (gap)."],
    ["2.2.2", "Pause, Stop, Hide", "A", PART, "Nothing but the decorative top-bar line and spinners moves for more than five seconds, and `prefers-reduced-motion` stops both. There is no control on the page itself (gap)."],
    ["2.3.1", "Three Flashes or Below Threshold", "A", SUP, "Nothing flashes."],
    ["2.4.1", "Bypass Blocks", "A", SUP, "A skip link is the first Tab stop, and the page has landmarks (journey: skip link)."],
    ["2.4.2", "Page Titled", "A", SUP, "Each page sets a title, and a page change is announced."],
    ["2.4.3", "Focus Order", "A", SUP, "Tab order follows reading order; focus moves to the page after a navigation and back to the opener after a dialog."],
    ["2.4.4", "Link Purpose (In Context)", "A", SUP, "axe-core's link-name rules pass on every page."],
    ["2.4.5", "Multiple Ways", "AA", SUP, "The areas bar, each area's pages, the search that finds every page, and Learn's search."],
    ["2.4.6", "Headings and Labels", "AA", SUP, "One h1 per page and heading order are checked on every page; fields have labels."],
    ["2.4.7", "Focus Visible", "AA", SUP, `The browser scan tabs through the first 25 stops of Home in both themes${s ? ` (${s.focusChecked} stops, ${s.focusMissing} without a ring)` : ""}; the ring is 2 pixels and 3:1.`],
    ["2.4.11", "Focus Not Obscured (Minimum)", "AA", SUP, "The sticky bars are accounted for with scroll padding. Not machine tested."],
    ["2.5.1", "Pointer Gestures", "A", SUP, "No multi-point or path gestures."],
    ["2.5.2", "Pointer Cancellation", "A", SUP, "Controls are native and act on release."],
    ["2.5.3", "Label in Name", "A", SUP, "axe-core's label-content-name-mismatch rule passes in the browser scan."],
    ["2.5.4", "Motion Actuation", "A", NA, "Nothing responds to device motion."],
    ["2.5.7", "Dragging Movements", "AA", SUP, "No action needs dragging."],
    ["2.5.8", "Target Size (Minimum)", "AA", SUP, "axe-core's target-size rule passes in the browser scan; targets are 24 pixels or more, 44 on a touch screen."],
    ["Understandable"],
    ["3.1.1", "Language of Page", "A", SUP, "`lang=\"en\"` on the page (journey)."],
    ["3.1.2", "Language of Parts", "AA", NA, "The interface is in English only."],
    ["3.2.1", "On Focus", "A", SUP, "Focus changes nothing."],
    ["3.2.2", "On Input", "A", SUP, "Changing a field submits nothing."],
    ["3.2.3", "Consistent Navigation", "AA", SUP, "One top bar, one area list on every page."],
    ["3.2.4", "Consistent Identification", "AA", SUP, "The same kit parts and words are used for the same things."],
    ["3.2.6", "Consistent Help", "A", SUP, "The Learn and Wingman buttons are in the same place on every page (journey)."],
    ["3.3.1", "Error Identification", "A", SUP, "A field in error is marked `aria-invalid` and its message is tied to it and announced."],
    ["3.3.2", "Labels or Instructions", "A", SUP, "Fields have visible labels and help."],
    ["3.3.3", "Error Suggestion", "AA", SUP, "The API says what to do (`next`), and the interface shows it."],
    ["3.3.4", "Error Prevention (Legal, Financial, Data)", "AA", PART, "Deciding an approval asks for a reason and says what each answer does; deleting some objects does not ask again (gap)."],
    ["3.3.7", "Redundant Entry", "A", SUP, "Nothing already entered in a flow is asked for again. Not machine tested."],
    ["3.3.8", "Accessible Authentication (Minimum)", "AA", SUP, "Sign-in is by the organization's single sign-on; Launchway sets no puzzle or recall test. A token can be pasted."],
    ["Robust"],
    ["4.1.2", "Name, Role, Value", "A", SUP, "axe-core's ARIA rules, on every kit part and page. Tabs, dialogs, menus, the search combobox and the Wingman panel follow the WAI-ARIA patterns (journeys check names, roles and states from the accessibility tree)."],
    ["4.1.3", "Status Messages", "AA", SUP, "A page change, a run that fails, an approval that needs a person, a decision's outcome and a session that ends are announced in live regions without moving focus."],
  ];
  const lines = [];
  lines.push("# Accessibility conformance statement", "");
  lines.push("A statement in the form of the Voluntary Product Accessibility Template (VPAT): each criterion of WCAG 2.2 levels A and AA, whether the web interface supports it, and what is behind the answer. It is a draft, written by the people who build the product from what is tested, not an audit by someone else. It is for people who buy, review or use Launchway. The plain-language page is [Accessibility](../guide/accessibility.md).", "");
  lines.push("This page is generated by `web/scripts/a11y-conformance.mjs` from the contrast test, the last browser scan and keyboard run, and the baselines of open findings; `make check` fails if it is out of date. The judgments in the remarks are written beside each criterion in that script.", "");
  lines.push("## Product", "");
  lines.push("| Item | Detail |", "|---|---|", "| Product | Launchway web interface |", "| Standard | WCAG 2.2 level AA (which Section 508 and EN 301 549 point to for web content) |", `| Last recorded run | ${s ? s.date : "not recorded yet"} |`, `| Open findings | ${open === 0 ? "none" : `${open} (${openRules.join(", ")})`} |`, "| Not evaluated | A screen reader on a physical device, Windows high contrast on Windows, Safari and Firefox (the scans use Chromium) |", "");
  lines.push("Terms: **Supports** means no known problem and tested as far as the remarks say. **Partially supports** means a known gap, named. **Does not support** means the criterion is not met. **Not applicable** means the product has nothing the criterion is about.", "");
  lines.push("## Results by criterion", "");
  let tableOpen = false;
  for (const c of C) {
    if (c.length === 1) {
      lines.push("", `### ${c[0]}`, "", "| Criterion | Level | Conformance | Remarks |", "|---|---|---|---|");
      tableOpen = true;
      continue;
    }
    void tableOpen;
    lines.push(`| ${c[0]} ${c[1]} | ${c[2]} | ${c[3]} | ${c[4].replace(/\|/g, "/")} |`);
  }
  const count = (k) => C.filter((c) => c.length > 1 && c[3] === k).length;
  lines.push("", "## Summary", "", `Of ${C.filter((c) => c.length > 1).length} criteria: ${count(SUP)} supported, ${count(PART)} partially supported, ${count(NO)} not supported, ${count(NA)} not applicable.`, "");
  lines.push("## Contrast results", "", `Computed by \`web/src/styles/contrast.test.ts\` from \`web/src/styles/tokens.css\`. The lowest ratio of each kind, by theme:`, "", "| Kind | Needed | Light | Dark |", "|---|---|---|---|");
  for (const [kind, label, need] of [["text", "Text on its background", 4.5], ["ui", "Icons, status marks and control borders", 3], ["focus", "The focus ring", 3]]) {
    const low = (theme) => Math.min(...rows.filter((r) => r.theme === theme && r.kind === kind).map((r) => r.ratio));
    lines.push(`| ${label} | ${need}:1 | ${low("light")}:1 | ${low("dark")}:1 |`);
  }
  void text;
  lines.push("", "## Report a problem", "", "Open an issue in the project's repository with the page, what you tried, what happened and the browser and assistive technology you use. See [Accessibility](../guide/accessibility.md#report-a-problem).", "");
  return lines.join("\n");
}

export function inputs() {
  const snapshot = existsSync(snapPath) ? JSON.parse(readFileSync(snapPath, "utf8")) : null;
  const jsdom = JSON.parse(readFileSync(join(here, "..", "src", "a11y", "baseline.json"), "utf8")).pages.map((p) => p.split("|")[1]);
  const browser = JSON.parse(readFileSync(join(here, "..", "a11y-browser-baseline.json"), "utf8")).findings.map((p) => p.split("|")[1]);
  return { snapshot, baselines: { jsdom, browser }, rows: evaluate(readTokens()) };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  if (args[0] === "--snapshot") {
    const report = JSON.parse(readFileSync(args[1], "utf8"));
    const kb = JSON.parse(readFileSync(args[2], "utf8"));
    const axeFindings = report.results.filter((r) => r.help && !r.baselined).length;
    const focus = report.results.filter((r) => r.rule === "focus-visible");
    writeFileSync(snapPath, JSON.stringify({
      date: new Date().toISOString().slice(0, 10), pages: report.pages.length, views: report.views, themes: report.themes,
      axeFindings, reflowPages: report.results.filter((r) => r.rule === "reflow").length,
      focusChecked: focus.reduce((n, r) => n + r.checked, 0), focusMissing: focus.reduce((n, r) => n + r.without, 0),
      journeysPassed: kb.results.filter((r) => r.ok).length, journeysTotal: kb.results.length,
    }, null, 1) + "\n");
  }
  const page = build(inputs()).replace(/\n{3,}/g, "\n\n").trimEnd() + "\n";
  if (args[0] === "--check") {
    if (!existsSync(pagePath) || readFileSync(pagePath, "utf8") !== page) {
      console.error("docs/reference/accessibility-conformance.md is not current: run node web/scripts/a11y-conformance.mjs");
      process.exit(1);
    }
  } else {
    writeFileSync(pagePath, page);
    console.log("wrote " + pagePath);
  }
}
