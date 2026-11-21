/// <reference types="vite/client" />
import { describe, expect, it } from "vitest";

// Every view has an address (docs/contributing/views-have-addresses.md): a page that shows tabs must keep the chosen tab
// in the address, through useTab or useUrlState. This reads the sources and names a page that does not.
const sources = import.meta.glob("./{pages,components}/**/*.tsx", { query: "?raw", import: "default", eager: true }) as Record<string, string>;
const files = (dir: string): string[] => Object.keys(sources).filter((f) => f.startsWith("./" + dir + "/"));
const readFileSync = (f: string, _enc?: string): string => sources[f];

// Pages whose tabs are not views of one thing (a wizard's steps, a one-time chooser) or are not yet converted. This list may only shrink.
const NOT_YET: string[] = [];

describe("views have addresses", () => {
  it("a page with tabs keeps the tab in the address", () => {
    const bad = files("pages").concat(files("components")).filter((f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx")).filter((f) => {
      const s = readFileSync(f, "utf8");
      const showsTabs = /<(Tabs|PageTabs)\b/.test(s) && !/export function (Tabs|PageTabs)\b/.test(s);
      return showsTabs && !/useTab\(|useUrlState\(|setUrl\(/.test(s) && !NOT_YET.some((n) => f.endsWith(n));
    });
    expect(bad, `these pages show tabs but keep the choice in state: use useTab or useUrlState (src/urlState.ts)`).toEqual([]);
  });
  it("a page does not write the address by hand for a view", () => {
    const hand = files("pages").filter((f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx")).filter((f) => /history\.replaceState\([^)]*location\.pathname \+/.test(readFileSync(f, "utf8")));
    expect(hand, "use setUrl or useUrlState instead of history.replaceState for a view").toEqual([]);
  });
});
