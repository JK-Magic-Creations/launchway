import { describe, expect, it } from "vitest";
import { approval, me, project, run } from "../test/fixtures";
import { approvalRefusal, commitLink, consequences, runSentence, trendOf } from "./decide";

describe("approvalRefusal", () => {
  const a = approval({ project: "shop-web", started_by: "sam" });
  it("says exactly why a starter cannot approve, and where an administrator changes it", () => {
    const r = approvalRefusal(a, me({ user: "sam", roles: { "shop-web": "operator" } }), { allow_self: false, sign_in: true });
    expect(r?.text).toBe("You started this Sortie. On this Base the person who starts a Sortie cannot give Clearance for it, so someone else must. You may still say No-go. An administrator can change this in Settings → Clearances.");
    expect(r?.link).toEqual({ href: "/settings/approvals", label: "Settings → Clearances" });
    expect(r?.mayReject).toBe(true);
  });
  it("lets the starter approve when the installation allows it", () => {
    expect(approvalRefusal(a, me({ user: "sam", roles: { "shop-web": "operator" } }), { allow_self: true, sign_in: true })).toBeNull();
  });
  it("lets another operator approve", () => {
    expect(approvalRefusal(a, me({ user: "dana", roles: { "shop-web": "operator" } }), { allow_self: false, sign_in: true })).toBeNull();
  });
  it("names the missing role for a viewer, who may not reject either", () => {
    const r = approvalRefusal(a, me({ user: "vic", roles: { "shop-web": "viewer" } }), { allow_self: true, sign_in: true });
    expect(r?.text).toContain("with the viewer role on shop-web");
    expect(r?.mayReject).toBe(false);
  });
});

describe("commitLink", () => {
  it("links to the commit on a host with a web address", () => {
    expect(commitLink(project({ url: "https://git.example/acme/shop.git" }), "abc")).toBe("https://git.example/acme/shop/commit/abc");
    expect(commitLink(project({ url: "git@git.example:acme/shop.git" }), "abc")).toBe("https://git.example/acme/shop/commit/abc");
  });
  it("has no link for a local repository", () => {
    expect(commitLink(project({ url: "/srv/repos/shop" }), "abc")).toBeNull();
    expect(commitLink(project({ url: "file:///srv/repos/shop" }), "abc")).toBeNull();
  });
});

describe("consequences", () => {
  it("names what goes ahead", () => {
    const d = { step_list: [{ id: "ship", needs: ["gate"] }, { id: "notify", needs: ["gate"] }, { id: "x", needs: [] }] } as never;
    expect(consequences(d, "gate").approve).toBe("ship and notify go ahead.");
    expect(consequences(null, "gate").approve).toBe("The Sortie goes on.");
  });
});

describe("trendOf and runSentence", () => {
  const rs = [run({ status: "failed", created_at: 5, started_at: 5, finished_at: 10_005 }), run({ status: "succeeded", started_at: 1, finished_at: 3_001 }), run({ status: "succeeded", started_at: 1, finished_at: 5_001 })];
  it("counts finished runs, oldest first", () => {
    const t = trendOf(rs);
    expect(t.ticks).toEqual(["ok", "ok", "bad"]);
    expect(t.rate).toBe(67);
    expect(t.median).toBe(5000);
  });
  it("is empty without runs", () => { expect(trendOf([]).rate).toBeNull(); });
  it("writes a sentence from the real steps", () => {
    expect(runSentence(run({ status: "failed", steps: [{ id: "build", status: "succeeded" }, { id: "test", status: "failed" }] }), "40s")).toBe("Stopped at test after 40s.");
    expect(runSentence(run({ status: "running", waiting: ["gate"] }), "")).toBe("Waiting for a person to decide: gate.");
    expect(runSentence(run({ status: "succeeded" }), "2m 1s")).toBe("Passed in 2m 1s.");
  });
});
