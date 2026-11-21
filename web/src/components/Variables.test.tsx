import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";
import type { VarEntry, VarView } from "../variablesApi";

const entry = (o: Partial<VarEntry>): VarEntry => ({
  name: "replicas", type: "number", value: 1, display: "1", from: { kind: "level", level: "installation" }, chain: [], why: "Set by the base.",
  set_here: false, can_override: true, ...o,
} as VarEntry);

const view = (vars: VarEntry[]): VarView => ({ rule: "The nearest setting wins: the rule.", level: "project", scope: "shop", words: {}, variables: vars });

vi.mock("../variablesApi", async (orig) => {
  const real = await orig<typeof import("../variablesApi")>();
  return { ...real, variablesApi: { ...real.variablesApi, project: vi.fn() } };
});
import { variablesApi } from "../variablesApi";
import { VariablesPanel } from "./Variables";

const show = (v: VarView) => {
  vi.mocked(variablesApi.project).mockResolvedValue(v);
  return render(<MeContext.Provider value={me({ admin: true })}><VariablesPanel level="project" scope="shop" /></MeContext.Provider>);
};

describe("the variables hierarchy", () => {
  it("guides a beginner when nothing is set", async () => {
    show(view([]));
    expect(await screen.findByText("No variables here yet.")).toBeInTheDocument();
    expect(screen.getByText(/You need none of this to run a pipeline/)).toBeInTheDocument();
  });

  it("shows where a value comes from, the overridden values struck through in order, and the lock", async () => {
    show(view([entry({
      display: "2", from: { kind: "level", level: "project", scope: "shop" }, set_here: true,
      chain: [
        { kind: "level", level: "project", scope: "shop", value: 2, display: "2" },
        { kind: "level", level: "unit", scope: "pay", value: 3, display: "3", overridden: true },
        { kind: "level", level: "installation", value: 1, display: "1", overridden: true },
      ],
    })]));
    expect(await screen.findByText("replicas")).toBeInTheDocument();
    const struck = document.querySelectorAll(".vr-chain s");
    expect([...struck].map((s) => s.textContent)).toEqual(["3", "1"]);
    expect(screen.getByText("from Project shop")).toBeInTheDocument();
  });

  it("disables override with the reason when a lock above forbids it, and names who locked it", async () => {
    show(view([entry({
      locked: { level: "installation", reason: "audited", who: "an administrator of every project" },
      can_override: false, blocked: "replicas is locked by the Base (audited). Only an administrator of every project can change it.",
    })]));
    expect(await screen.findByText(/Locked by Base/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Override here" })).toBeDisabled();
    expect(screen.getAllByText(/Only an administrator of every project can change it/).length).toBeGreaterThan(0);
  });

  it("shows a secret as its reference, and says it is one", async () => {
    show(view([entry({ name: "token", type: "secret", display: "vault:apps/x#token", value: { store: "vault", key: "apps/x" } })]));
    expect(await screen.findByText("vault:apps/x#token")).toBeInTheDocument();
    expect(screen.getByText("reference")).toBeInTheDocument();
  });
});
