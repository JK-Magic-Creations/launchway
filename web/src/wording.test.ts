import { describe, expect, it } from "vitest";

// No page tells a person to edit a file or restart a server to do something the
// interface should do (owner, 20 November). This reads the source of every screen.

const FORBIDDEN = [
  /edit\s+(the\s+)?launchway\.yaml/i,
  /restart\s+(the|your|each)?\s*(launchway\s+)?server/i,
  /restart\s+it\b/i,
  /edit\s+(the\s+|its\s+)?(server's\s+)?config(uration)?(\s+file)?\b/i,
  /add\s+\S+\s+to\s+`?launchway\.yaml/i,
  /ai\.models/i,
  /put these lines in the configuration file/i,
];

// The sign-in settings are read before the database can be, so the first-run
// wizard still has to say where they go. Tracked as a defect to remove, not a rule.
const KNOWN = new Set(["pages/wizards/FirstRun.tsx", "pages/ConfigHome.tsx"]);

const sources = import.meta.glob(["./**/*.ts", "./**/*.tsx", "!./**/*.test.ts", "!./**/*.test.tsx"], { query: "?raw", import: "default", eager: true }) as Record<string, string>;

describe("wording", () => {
  for (const [path, text] of Object.entries(sources)) {
    const rel = path.slice(2);
    if (KNOWN.has(rel)) continue;
    it(`${rel} tells nobody to edit a file or restart a server`, () => {
      for (const re of FORBIDDEN) expect(text.match(re)?.[0], `${rel} says "${text.match(re)?.[0]}"`).toBeUndefined();
    });
  }
  it("catches the phrases it is for", () => {
    for (const s of ["Edit launchway.yaml", "restart the server", "Restart the servers", "edit its configuration file and restart it"]) {
      expect(FORBIDDEN.some((re) => re.test(s)), s).toBe(true);
    }
  });
});
