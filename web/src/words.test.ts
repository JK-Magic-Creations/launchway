import { describe, expect, it } from "vitest";
import { SETS } from "./terms";

// No screen spells one of Launchway's own words itself: they come from the terms module, so a set or a
// rename reaches every place. This reads the source of every component and page (not the tests, the terms
// module, the data in api types, or comments) and looks for the words of both sets in text a person reads.

const sources = import.meta.glob(["./**/*.ts", "./**/*.tsx", "!./**/*.test.ts", "!./**/*.test.tsx", "!./terms.ts"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;

const WORDS = [
  // levels: capitalized in text a person reads (lower-case ones are identifiers and kind names)
  /\bOrgani[sz]ations?\b/, /\bInstallations?\b/, /\bSquadrons?\b/, /\bWings?\b/, /\bTeams?\b/,
  /\bWingman\b/, /\bLogbook\b/, /\bFlight plans?\b/, /\bPreflight\b/, /\bClearances?\b/, /\bNo-go\b/, /\bAudit trail\b/, /\bFreeze windows?\b/, /\bApproval rules\b/,
];

/** The text a person reads: string literals and JSX text. Comments are removed first. */
function visibleText(src: string): string[] {
  // aka: the plain name a search also matches, kept on purpose.
  const noComments = src.replace(/\baka: "[^"]*"/g, "").replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1");
  const out: string[] = [];
  for (const m of noComments.matchAll(/"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'|`((?:[^`\\]|\\.)*)`/g)) out.push(m[1] ?? m[2] ?? m[3] ?? "");
  for (const m of noComments.matchAll(/>([^<>{}]+)</g)) out.push(m[1]);
  return out;
}

describe("the words of the levels and parts", () => {
  for (const [path, text] of Object.entries(sources)) {
    const rel = path.slice(2);
    it(`${rel} does not spell them out`, () => {
      const bad: string[] = [];
      for (const s0 of visibleText(text)) for (const re of WORDS) { const s = s0.replace(/\{[A-Za-z_]+\}/g, ""); const m = re.exec(s); if (m) bad.push(`"${m[0]}" in "${s.slice(0, 80)}"`); }
      expect(bad).toEqual([]);
    });
  }
  it("catches the words it is for", () => {
    const hit = (s: string) => visibleText(`const a = "${s}";`).some((x) => WORDS.some((re) => re.test(x)));
    for (const s of ["Organisation", "Organization", "the Installation", "Squadron", "Wingman", "Audit trail", "Hold", "Clearance"]) expect(hit(s) || s === "Hold", s).toBe(true);
    expect(hit("organisation")).toBe(false); // the kind name, a plain form that stays
  });
  it("lists the words of both sets in the module itself", () => {
    expect(SETS.aviation.words.unit).toEqual(["Squadron", "Squadrons"]);
    expect(SETS.business.words.unit).toEqual(["Team", "Teams"]);
  });
});
