import { describe, expect, it } from "vitest";
import { safeNext } from "./signInNext";

describe("safeNext", () => {
  it("keeps paths on this server, with their query", () => {
    for (const p of ["/", "/deployments/releases/hub/0.1.0/get/hub-app.apk", "/a?b=c&d=e#f"]) expect(safeNext(p)).toBe(p);
  });
  it("turns everything that could leave the site into the start page", () => {
    for (const p of ["//evil.example", "/\\evil.example", "https://evil.example", "javascript:alert(1)", "evil", "", "/a b", "/a\nb", "/\t/evil.example", "/" + "x".repeat(2100)]) {
      expect(safeNext(p), p).toBe("/");
    }
    expect(safeNext(null)).toBe("/");
  });
});
