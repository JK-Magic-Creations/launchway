import { describe, expect, it } from "vitest";
import { versionProblem } from "./CutRelease";

describe("versionProblem", () => {
  it("accepts versions of the scheme and says plainly what is wrong with others", () => {
    for (const v of ["1.4.0", "0.1.0", "2.0.0-rc.1", "1.0.0+build.5"]) expect(versionProblem("semver", v)).toBe("");
    for (const v of ["v1.4.0", "1.4", "1.4.0.1", "one"]) expect(versionProblem("semver", v)).toMatch(/three numbers/);
    expect(versionProblem("semver", "  ")).toMatch(/Say which version/);
    expect(versionProblem("calver", "2026.11.22")).toBe("");
    expect(versionProblem("calver", "26.1")).toMatch(/year/);
    expect(versionProblem("natural", "r9")).toBe("");
    expect(versionProblem("natural", "-x")).not.toBe("");
  });
});
