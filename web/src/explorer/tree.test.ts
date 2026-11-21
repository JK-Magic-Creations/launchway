import { describe, expect, it } from "vitest";
import { buildTree, derivePath, highlight, itemScore, nodeKey, score, visibleNodes, worst, type XItem } from "./tree";

const item = (name: string, path: string[], tone: XItem["tone"] = "ok", labels: string[] = []): XItem => ({
  id: name, name, path: path.map((p) => ({ key: p, label: p })), labels, tone, history: [], running: 0, href: "/" + name, favorite: false,
});

describe("the Explorer tree", () => {
  const items = [item("api", ["Payments", "Services"], "bad"), item("ledger", ["Payments", "Services"], "ok"), item("web", ["Storefront"], "run"), item("docs", [])];
  it("counts items at every level and rolls the worst status up", () => {
    const t = buildTree(items);
    expect(t.get("")!.count).toBe(4);
    expect(t.get("Payments")!.count).toBe(2);
    expect(worst(t.get("Payments/Services")!.tones)).toBe("bad");
    expect(worst(t.get("Storefront")!.tones)).toBe("run");
    expect(nodeKey([{ key: "a", label: "A" }, { key: "b", label: "B" }])).toBe("a/b");
  });
  it("lists only the open branches", () => {
    const t = buildTree(items);
    expect(visibleNodes(t, new Set()).map((n) => n.key)).toEqual(["Payments", "Storefront"]);
    expect(visibleNodes(t, new Set(["Payments"])).map((n) => n.key)).toEqual(["Payments", "Payments/Services", "Storefront"]);
  });
  it("keeps empty folders so an item can be filed into them", () => {
    const t = buildTree(items, [{ path: [{ key: "Payments", label: "Payments" }, { key: "New", label: "New", folderId: "f1" }] }]);
    expect(t.get("Payments/New")!.count).toBe(0);
    expect(t.get("Payments/New")!.folderId).toBe("f1");
  });
  it("derives folders from a path-like name", () => {
    expect(derivePath("payments/api/build")).toMatchObject({ leaf: "build", path: [{ label: "payments" }, { label: "api" }] });
    expect(derivePath("plain")).toEqual({ path: [], leaf: "plain" });
  });
});

describe("matching", () => {
  it("ranks exact over prefix over word over substring, and forgives a typo", () => {
    expect(score("pay", "pay")).toBeGreaterThan(score("pay", "pay-api"));
    expect(score("pay", "pay-api")).toBeGreaterThan(score("api", "pay-api"));
    expect(score("api", "pay-api")).toBeGreaterThan(score("ay-a", "pay-api"));
    expect(score("paymnets", "payments")).toBeGreaterThan(0);
    expect(score("zzz", "payments")).toBe(0);
  });
  it("matches name, labels and place, and needs every word", () => {
    const it = item("ledger", ["Payments"], "ok", ["tier-1", "go"]);
    expect(itemScore("ledger", it)).toBeGreaterThan(0);
    expect(itemScore("tier-1", it)).toBeGreaterThan(0);
    expect(itemScore("payments go", it)).toBeGreaterThan(0);
    expect(itemScore("payments rust", it)).toBe(0);
    expect(itemScore("", it)).toBeGreaterThan(0);
  });
  it("splits text for highlighting", () => {
    expect(highlight("pay-api", "api")).toEqual([["pay-", false], ["api", true]]);
    expect(highlight("abc", "")).toEqual([["abc", false]]);
  });
});
