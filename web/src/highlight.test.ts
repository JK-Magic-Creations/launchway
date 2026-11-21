import { describe, expect, it } from "vitest";
import { highlight, languageOf } from "./highlight";

describe("the highlighter", () => {
  it("names a language by file name and shows an unknown one plain", () => {
    expect(languageOf("cmd/main.go")).toBe("go");
    expect(languageOf("Makefile")).toBe("mk");
    expect(languageOf("notes.xyz")).toBe("");
    expect(highlight(["a < b"], "")[0]).toEqual([{ k: "", t: "a < b" }]);
  });
  it("splits comments, strings, numbers and keywords, and keeps a block comment open across lines", () => {
    const r = highlight(["func f() { return 42 } // done", "/* open", "still */ x := \"a\\\"b\""], "go");
    expect(r[0].some((x) => x.k === "k" && x.t === "func")).toBe(true);
    expect(r[0].some((x) => x.k === "n" && x.t === "42")).toBe(true);
    expect(r[0].at(-1)).toEqual({ k: "c", t: "// done" });
    expect(r[1][0].k).toBe("c");
    expect(r[2][0]).toEqual({ k: "c", t: "still */" });
    expect(r[2].some((x) => x.k === "s" && x.t === "\"a\\\"b\"")).toBe(true);
  });
  it("never loses or invents a character", () => {
    const text = ["<script>alert(\"x\")</script>", "# not code", "'unterminated"];
    for (const lang of ["html", "py", "go", "json", "sh"]) {
      const out = highlight(text, lang);
      expect(out.map((l) => l.map((r) => r.t).join(""))).toEqual(text);
    }
  });
});
