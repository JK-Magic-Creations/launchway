import { describe, expect, it } from "vitest";
import type { StepView } from "../api";
import { step } from "../test/fixtures";
import { buildNodes, foldableChains, groupStatus, nodeHolding, stageSentences } from "./model";

const s = (id: string, needs: string[] = [], more: Partial<StepView> = {}) => step({ id, needs, ...more });
const none = { groups: new Set<string>(), chains: new Set<string>() };

describe("the nodes of the graph", () => {
  const steps = [s("check-code"), s("test", ["check-code"]), s("security-go", ["check-code"]), s("security-web", ["check-code"]), s("image", ["test", "security-go", "security-web"])];
  it("says the stages in words, with the parallel ones named together", () => {
    expect(stageSentences(buildNodes(steps, undefined, none, new Map()))).toEqual([
      "Stage 1: check-code", "Stage 2 (runs together): test, security-go, security-web", "Stage 3: image"]);
  });
  it("folds a template group into one node that needs what its steps need", () => {
    const g = [s("a"), s("build/compile", ["a"], { group: "build" }), s("build/pack", ["build/compile"], { group: "build" }), s("z", ["build/pack"])];
    const nodes = buildNodes(g, [{ id: "build", uses: "go/build", library: "go", template: "build", steps: ["build/compile", "build/pack"] }], { groups: new Set(["build"]), chains: new Set() }, new Map());
    expect(nodes.map((n) => [n.id, n.needs])).toEqual([["a", []], ["group:build", ["a"]], ["z", ["group:build"]]]);
    expect(nodeHolding(nodes, "build/pack")).toBe("group:build");
  });
  it("folds the inside of a long chain, keeping its first and last step", () => {
    const line = Array.from({ length: 7 }, (_, i) => s("c" + i, i ? ["c" + (i - 1)] : []));
    const chains = foldableChains(line);
    expect([...chains.keys()]).toEqual(["c1"]);
    const nodes = buildNodes(line, undefined, { groups: new Set(), chains: new Set(["c1"]) }, chains);
    expect(nodes.map((n) => n.id)).toEqual(["c0", "chain:c1", "c6"]);
    expect(nodes[1].steps).toHaveLength(5);
    expect(nodes[2].needs).toEqual(["chain:c1"]);
  });
  it("sums up a group's status", () => {
    expect(groupStatus(["succeeded", "running", "pending"])).toBe("running");
    expect(groupStatus(["succeeded", "failed", "skipped"])).toBe("failed");
    expect(groupStatus(["skipped", "skipped"])).toBe("skipped");
  });
});
