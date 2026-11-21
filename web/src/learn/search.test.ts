import { describe, expect, it } from "vitest";
import type { SearchIndex } from "./content";
import { search, words } from "./search";

const idx: SearchIndex = {
  pages: ["concepts/approvals", "guide/pipelines"],
  chunks: [
    [0, "how-it-works", "How it works", "A pipeline has a step of kind approval"],
    [0, "who-may-decide", "Who may decide", "The person who started a run cannot approve it"],
    [1, "", "", "The pipeline file lists steps"],
  ],
  terms: { approval: [0, 4, 1, 1], approve: [1, 1], decide: [1, 4], pipeline: [0, 1, 2, 3], steps: [2, 1], step: [0, 1], "pipeline.yaml": [2, 1] },
};

describe("search", () => {
  it("cuts words like the build does", () => {
    expect(words("The pipeline.yaml file, and the Steps!")).toEqual(["pipeline.yaml", "pipeline", "yaml", "file", "steps"]);
  });
  it("finds a section by its words, best first", () => {
    const hits = search(idx, "approval");
    expect(hits[0]).toMatchObject({ slug: "concepts/approvals", anchor: "how-it-works" });
  });
  it("finishes the last word", () => {
    expect(search(idx, "deci")[0].anchor).toBe("who-may-decide");
  });
  it("needs every word, when some section has them all", () => {
    expect(search(idx, "approval pipeline").map((h) => h.anchor)).toEqual(["how-it-works"]);
  });
  it("falls back to the sections that have most of the words", () => {
    expect(search(idx, "approval zzz").length).toBeGreaterThan(0);
    expect(search(idx, "zzz")).toEqual([]);
    expect(search(idx, "")).toEqual([]);
  });
  it("shows each page once before it shows a page again", () => {
    const hits = search(idx, "pipeline");
    expect(hits.map((h) => h.slug)).toEqual(["guide/pipelines", "concepts/approvals"]);
  });
});
