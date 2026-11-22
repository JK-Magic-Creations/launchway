import { describe, expect, it } from "vitest";
import { parseRanges, parseRepositories } from "./tokenLimits";

describe("repositories", () => {
  it("takes owner/name and owner/*", () => {
    expect(parseRepositories("acme/shop, acme/*  other/x")).toEqual({ ok: true, values: ["acme/shop", "acme/*", "other/x"] });
    expect(parseRepositories("")).toEqual({ ok: true, values: [] });
  });
  it("refuses anything else, plainly", () => {
    for (const bad of ["acme", "acme/sh*", "*/shop", "Acme/shop", "acme/shop/x", "acme//shop", "../x/y"]) {
      const r = parseRepositories(bad);
      expect(r.ok, bad).toBe(false);
    }
  });
});

describe("source ranges", () => {
  it("takes CIDR ranges and single addresses", () => {
    expect(parseRanges("203.0.113.0/24, 2001:db8::/32 198.51.100.7")).toEqual({ ok: true, values: ["203.0.113.0/24", "2001:db8::/32", "198.51.100.7"] });
  });
  it("refuses what is not an address range", () => {
    for (const bad of ["nonsense", "10.0.0.0/33", "10.0.0.0/0", "300.1.1.1", "10.0.0/8", "::/129", "10.0.0.0/8/8"]) {
      const r = parseRanges(bad);
      expect(r.ok, bad).toBe(false);
      if (!r.ok) expect(r.problem).toContain("is not an address range");
    }
  });
});
