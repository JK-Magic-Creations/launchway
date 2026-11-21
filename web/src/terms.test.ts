import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_SET, SETS, TERM_META, makeTerms, resetTerms, setTerms, termProblem, terms } from "./terms";

describe("the terms module", () => {
  afterEach(() => resetTerms());

  it("defaults to the aviation words", () => {
    const t = makeTerms();
    expect(DEFAULT_SET).toBe("aviation");
    expect([t.label("installation"), t.label("organization"), t.label("unit"), t.label("project")]).toEqual(["Base", "Wing", "Squadron", "Project"]);
    expect(t.label("guide")).toBe("Wingman");
    expect([t.label("clearance"), t.label("go"), t.label("no_go"), t.label("hold")]).toEqual(["Clearance", "Go", "No-go", "Hold"]);
    expect([t.label("flight_plan"), t.label("logbook"), t.label("preflight"), t.label("payload")]).toEqual(["Flight plan", "Logbook", "Preflight", "Payload"]);
  });

  it("switches to the plain business set", () => {
    const t = makeTerms({ set: "business" });
    expect([t.label("installation"), t.label("organization"), t.label("unit"), t.label("project")]).toEqual(["Installation", "Organization", "Team", "Project"]);
    expect([t.label("guide"), t.label("preflight"), t.label("clearance"), t.label("go"), t.label("no_go")]).toEqual(["Guide", "Checks", "Approval", "Approve", "Reject"]);
    expect([t.label("hold"), t.label("flight_plan"), t.label("logbook")]).toEqual(["Freeze", "Starter pipeline", "Audit trail"]);
  });

  it("uses American spelling in both sets", () => {
    for (const s of Object.values(SETS)) for (const w of Object.values(s.words)) expect(JSON.stringify(w)).not.toMatch(/isation|organis/i);
  });

  it("has a label for every term the editor lists, in both sets", () => {
    for (const s of Object.values(SETS)) for (const m of TERM_META) expect(s.words[m.key], m.key).toBeTruthy();
  });

  it("renames a single term, and the rest keep the set's word", () => {
    const t = makeTerms({ set: "aviation", overrides: { guide: "Co-pilot", unit: "Flight" } });
    expect(t.label("guide")).toBe("Co-pilot");
    expect(t.label("unit")).toBe("Flight");
    expect(t.plural("unit")).toBe("Flights");
    expect(t.label("organization")).toBe("Wing");
  });

  it("takes a plural of its own and forms regular plurals", () => {
    expect(makeTerms({ overrides: { organization: "Company" } }).plural("organization")).toBe("Companies");
    expect(makeTerms({ overrides: { unit: "Crew", unit_plural: "Crews!" } }).plural("unit")).toBe("Crews!");
    expect(makeTerms({}).plural("installation")).toBe("Bases");
  });

  it("gives a readable fallback for a key it does not know", () => {
    const t = makeTerms();
    expect(t.label("some_new_thing")).toBe("Some new thing");
    expect(t.lower("some_new_thing")).toBe("some new thing");
    expect(t.say("keep {nothing_known} here")).toBe("keep {nothing_known} here");
  });

  it("falls back to the default set for an unknown set name", () => {
    expect(makeTerms({ set: "pirate" }).label("guide")).toBe("Wingman");
  });

  it("fills a sentence: {clearance}, {Clearance} and {clearances}", () => {
    const t = makeTerms();
    expect(t.say("Waiting for {clearance}. {Hold} windows. All {clearances}.")).toBe("Waiting for clearance. Hold windows. All clearances.");
    expect(makeTerms({ set: "business" }).say("a {hold} window and the {logbook}")).toBe("a freeze window and the audit trail");
  });

  it("keeps a capital inside a renamed word (a name or an acronym) in the middle of a sentence", () => {
    expect(makeTerms({ overrides: { guide: "AskOps" } }).say("the {guide}")).toBe("the AskOps");
  });

  it("counts a level name an older installation set, under the set and below a rename", () => {
    const t = makeTerms({ set: "aviation", levels: { unit: { singular: "Group", plural: "Groups" } }, overrides: { project: "Service" } });
    expect(t.label("unit")).toBe("Group");
    expect(t.label("project")).toBe("Service");
    // The server's own built-in names are not an override: the chosen set shows through.
    expect(makeTerms({ set: "business", levels: { unit: { singular: "Unit", plural: "Units" }, organisation: { singular: "Organisation", plural: "Organisations" } } }).label("unit")).toBe("Team");
  });

  it("is global once set, and says so to whoever listens", () => {
    setTerms({ set: "business" });
    expect(terms().label("go")).toBe("Approve");
    resetTerms();
    expect(terms().label("go")).toBe("Go");
  });

  it("refuses a label the server would refuse", () => {
    expect(termProblem("Flight")).toBeNull();
    expect(termProblem("")).not.toBeNull();
    expect(termProblem("<b>x</b>")).not.toBeNull();
    expect(termProblem("x".repeat(41))).not.toBeNull();
  });
});
