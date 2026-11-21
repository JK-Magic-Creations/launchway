import { afterEach, describe, expect, it } from "vitest";
import { DEFAULT_SET, makeTerms, renameableKeys, resetTerms, setTerms, termProblem, terms, vocabulary } from "./terms";

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
    for (const w of vocabulary()) expect(JSON.stringify([w.aviation, w.plain])).not.toMatch(/isation|organis/i);
  });

  it("has both sets, a plain meaning and a capitalized name for every word", () => {
    expect(vocabulary().length).toBeGreaterThan(50);
    for (const w of vocabulary()) {
      expect(w.meaning, w.key).not.toBe("");
      expect(w.aviation.one[0], w.key).toMatch(/[A-Z]/);
      expect(w.plain.one[0], w.key).toMatch(/[A-Z]/);
    }
    expect(renameableKeys()).not.toContain("running");
  });

  it("gives the ruling's words in the aviation set and plain ones in the business set", () => {
    const a = makeTerms(), b = makeTerms({ set: "business" });
    const pairs: [string, string, string][] = [["runner", "Station", "Runner"], ["run", "Sortie", "Run"], ["queue", "Holding pattern", "Queue"], ["run_log", "Flight recorder", "Run log"],
      ["debrief", "Debrief", "Run summary"], ["schedule", "Patrol", "Scheduled run"], ["deploy", "Launch", "Deploy"], ["environment", "Airfield", "Environment"],
      ["rollback", "Return to base", "Rollback"], ["emergency_rollback", "Abort", "Emergency rollback"], ["canary", "Pathfinder", "Canary"], ["post_deploy_watch", "Overwatch", "Post-deploy watch"],
      ["toggle", "Switch", "Feature toggle"], ["kill_switch", "Master switch", "Kill switch"], ["release_unit", "Formation", "Release group"], ["home", "Tower", "Home"],
      ["monitoring", "Radar", "Monitoring"], ["notifications", "Comms", "Notifications"], ["registry", "Depot", "Registry"], ["artifact", "Cargo", "Artifact"],
      ["standards", "Standing orders", "Standards"], ["scan", "Inspection", "Security scan"], ["dependency_updates", "Maintenance", "Dependency updates"], ["chaos_test", "Drill", "Chaos test"],
      ["learn", "Flight school", "Learn"], ["owner", "Commander", "Owner"], ["approver", "Controller", "Approver"], ["developer", "Pilot", "Developer"], ["viewer", "Observer", "Viewer"],
      ["queued", "Holding", "Queued"], ["running", "Airborne", "Running"], ["succeeded", "Mission complete", "Succeeded"], ["canceled", "Scrubbed", "Canceled"], ["rolled_back", "Returned to base", "Rolled back"]];
    for (const [k, av, pl] of pairs) { expect(a.word(k), k).toBe(av); expect(b.word(k), k).toBe(pl); expect(a.plain(k)).toBe(pl); }
    expect(a.plural("run")).toBe("Sorties");
    expect(a.plural("queue")).toBe("Holding patterns");
  });

  it("has verb forms and button words", () => {
    const a = makeTerms(), b = makeTerms({ set: "business" });
    expect([a.verb("deploy"), a.verb("deploy", "did"), a.verb("deploy", "doing")]).toEqual(["launch", "launched", "launching"]);
    expect([a.verb("rollback"), a.verb("rollback", "did"), a.verb("rollback", "doing")]).toEqual(["return to base", "returned to base", "returning to base"]);
    expect(a.action("deploy")).toBe("Launch");
    expect(a.action("rollback")).toBe("Return to base");
    expect(a.action("emergency_rollback")).toBe("Abort");
    expect(b.action("deploy")).toBe("Deploy");
    expect(b.verb("rollback", "did")).toBe("rolled back");
    // A rename replaces the verb too: the installation's word is used as written.
    expect(makeTerms({ overrides: { deploy: "Ship" } }).verb("deploy", "did")).toBe("ship");
  });

  it("is capitalized as a name in the middle of a sentence in the aviation set, and plain English in the business set", () => {
    expect(makeTerms().lower("run")).toBe("Sortie");
    expect(makeTerms().lower("project")).toBe("Project");
    expect(makeTerms({ set: "business" }).lower("run")).toBe("run");
    expect(makeTerms({ set: "business" }).lower("unit")).toBe("Team");
    expect(makeTerms().say("each {run} on a {runner}; {Runs}; {deploy:doing}; {run:plain}")).toBe("each Sortie on a Station; Sorties; launching; run");
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
    expect(t.say("Waiting for {clearance}. {Hold} windows. All {clearances}.")).toBe("Waiting for Clearance. Hold windows. All Clearances.");
    expect(makeTerms({ set: "business" }).say("a {hold} window and the {logbook}")).toBe("a freeze window and the audit trail");
  });

  it("keeps a capital inside a renamed word (a name or an acronym) in the middle of a sentence", () => {
    expect(makeTerms({ set: "business", overrides: { guide: "AskOps" } }).say("the {guide}")).toBe("the AskOps");
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
