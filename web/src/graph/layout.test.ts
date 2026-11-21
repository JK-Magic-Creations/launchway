import { describe, expect, it } from "vitest";
import { DEFAULTS, depths, layoutGraph, serialChains, stages, type GNode, type GraphLayout } from "./layout";

const n = (id: string, ...needs: string[]): GNode => ({ id, needs });

// Launchway's own pipeline: three steps run together after check-code.
const OWN = [n("check-code"), n("test", "check-code"), n("security-go", "check-code"), n("security-web", "check-code"),
  n("build-ui", "test"), n("image", "build-ui", "security-go", "security-web"), n("deploy", "image")];

function col(l: GraphLayout, id: string) { return l.nodes.find((x) => x.id === id)!.col; }

describe("stages", () => {
  it("puts steps that do not wait for each other in one stage", () => {
    expect(stages(OWN)).toEqual([["check-code"], ["test", "security-go", "security-web"], ["build-ui"], ["image"], ["deploy"]]);
  });
  it("uses the longest path, ignores unknown needs and survives a loop", () => {
    expect(depths([n("a"), n("b", "a"), n("c", "a", "b", "ghost")]).get("c")).toBe(2);
    expect(depths([n("a", "b"), n("b", "a")]).size).toBe(2);
    expect(stages([])).toEqual([]);
  });
});

describe("layoutGraph", () => {
  it("stacks parallel steps in one column, centred on the step before them", () => {
    const l = layoutGraph(OWN);
    expect(new Set(["test", "security-go", "security-web"].map((id) => col(l, id)))).toEqual(new Set([1]));
    const ys = ["test", "security-go", "security-web"].map((id) => l.nodes.find((x) => x.id === id)!.y);
    expect(new Set(ys).size).toBe(3);
    const root = l.nodes.find((x) => x.id === "check-code")!;
    const mid = (Math.min(...ys) + Math.max(...ys) + DEFAULTS.nodeH) / 2;
    expect(root.y + root.h / 2).toBeCloseTo(mid, 5);
    expect(l.crossings).toBe(0);
  });
  it("draws a diamond without crossings and a wide fan-out as one column of eight", () => {
    const diamond = layoutGraph([n("a"), n("b", "a"), n("c", "a"), n("d", "b", "c")]);
    expect(diamond.crossings).toBe(0);
    expect(diamond.columns.map((c) => c.ids.length)).toEqual([1, 2, 1]);
    const fan = layoutGraph([n("root"), ...Array.from({ length: 8 }, (_, i) => n("p" + i, "root")), n("join", ...Array.from({ length: 8 }, (_, i) => "p" + i))]);
    expect(fan.columns[1].ids).toHaveLength(8);
    expect(fan.crossings).toBe(0);
  });
  it("routes a connector that skips columns through a slot in each, between steps", () => {
    const l = layoutGraph([n("a"), n("b", "a"), n("c", "b"), n("d", "c", "a")]);
    const skip = l.edges.find((e) => e.from === "a" && e.to === "d")!;
    expect(skip.span).toBe(3);
    expect(skip.points).toHaveLength(2 + 2 * 2);
    expect(skip.d).toContain("L");
    for (const nd of l.nodes) {
      for (const p of skip.points.slice(1, -1)) {
        const inside = p.x > nd.x && p.x < nd.x + nd.w && p.y > nd.y && p.y < nd.y + nd.h;
        expect(inside, `the skip passes through ${nd.id}`).toBe(false);
      }
    }
  });
  it("orders a column to avoid crossings", () => {
    // x1→y2 and x2→y1 would cross if y1 stayed first.
    const l = layoutGraph([n("x1"), n("x2"), n("y1", "x2"), n("y2", "x1")]);
    expect(l.crossings).toBe(0);
  });
  it("gives the neighbours in drawing order, for keyboard movement", () => {
    const l = layoutGraph(OWN);
    expect(l.after["check-code"]).toHaveLength(3);
    expect(l.before["image"].sort()).toEqual(["build-ui", "security-go", "security-web"]);
  });
  it("handles an empty pipeline and a single step", () => {
    expect(layoutGraph([]).nodes).toEqual([]);
    expect(layoutGraph([n("only")]).columns).toHaveLength(1);
  });
});

// A small deterministic generator, so a failure can be reproduced from its seed.
function random(seed: number) { return () => { seed = (seed * 1664525 + 1013904223) % 4294967296; return seed / 4294967296; }; }
function randomGraph(seed: number): GNode[] {
  const r = random(seed);
  const size = 1 + Math.floor(r() * 24);
  return Array.from({ length: size }, (_, i) => n("s" + i, ...Array.from({ length: i }, (_, j) => "s" + j).filter(() => r() < 0.22)));
}

describe("layoutGraph, for 300 random pipelines", () => {
  const graphs = Array.from({ length: 300 }, (_, i) => ({ seed: i + 1, g: randomGraph(i + 1) }));
  it("sends every connector from left to right", () => {
    for (const { seed, g } of graphs) {
      const l = layoutGraph(g);
      for (const e of l.edges) {
        expect(col(l, e.from) < col(l, e.to), `seed ${seed}: ${e.from} to ${e.to}`).toBe(true);
        for (let i = 1; i < e.points.length; i++) expect(e.points[i].x >= e.points[i - 1].x, `seed ${seed}: path of ${e.from}>${e.to}`).toBe(true);
      }
    }
  });
  it("never overlaps two steps and keeps them inside the drawing", () => {
    for (const { seed, g } of graphs) {
      const l = layoutGraph(g);
      for (let i = 0; i < l.nodes.length; i++) {
        const a = l.nodes[i];
        expect(a.x >= 0 && a.y >= 0 && a.x + a.w <= l.width && a.y + a.h <= l.height, `seed ${seed}: ${a.id} is outside`).toBe(true);
        for (let j = i + 1; j < l.nodes.length; j++) {
          const b = l.nodes[j];
          const apart = a.x + a.w <= b.x || b.x + b.w <= a.x || a.y + a.h <= b.y || b.y + b.h <= a.y;
          expect(apart, `seed ${seed}: ${a.id} overlaps ${b.id}`).toBe(true);
        }
      }
    }
  });
  it("puts steps with the same needs in the same column, and every step one column after what it needs", () => {
    for (const { seed, g } of graphs) {
      const l = layoutGraph(g);
      const key = (x: GNode) => [...x.needs].sort().join(",");
      for (const a of g) for (const b of g) if (key(a) === key(b)) expect(col(l, a.id), `seed ${seed}: ${a.id} and ${b.id}`).toBe(col(l, b.id));
      for (const x of g) {
        const want = x.needs.length ? Math.max(...x.needs.map((d) => col(l, d))) + 1 : 0;
        expect(col(l, x.id), `seed ${seed}: ${x.id}`).toBe(want);
      }
    }
  });
  it("is stable: the same pipeline gives the same drawing", () => {
    for (const { g } of graphs.slice(0, 40)) expect(layoutGraph(g)).toEqual(layoutGraph(g));
  });
});

describe("serialChains", () => {
  const line = (k: number) => Array.from({ length: k }, (_, i) => n("c" + i, ...(i ? ["c" + (i - 1)] : [])));
  it("finds a long run of steps with nothing beside them", () => {
    expect(serialChains(line(6))).toEqual([["c0", "c1", "c2", "c3", "c4", "c5"]]);
    expect(serialChains(line(4))).toEqual([]);
  });
  it("breaks a chain where it fans out or in", () => {
    const g = [...line(5), n("side", "c1"), n("end", "c4", "side")];
    expect(serialChains(g)).toEqual([]);
    expect(serialChains(OWN)).toEqual([]);
  });
});
