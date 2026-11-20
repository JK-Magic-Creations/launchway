import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RunDetail } from "../api";
import { MeContext } from "../auth";
import { me, step } from "../test/fixtures";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, verifyApi: { ...real.verifyApi, models: vi.fn().mockResolvedValue({ models: [], server_can_explain: true }), explanations: vi.fn().mockResolvedValue([]) } };
});

import { ExplainFailure } from "./Explain";

const runDetail = { id: "run_1", project: "shop-api" } as RunDetail;
const view = (m: ReturnType<typeof me>) => render(<MeContext.Provider value={m}><ExplainFailure run={runDetail} step={step()} /></MeContext.Provider>);

beforeEach(() => { vi.resetModules(); });

describe("explaining a failure with no AI model", () => {
  it("offers an administrator the wizard and says what it does", async () => {
    view(me({ admin: true }));
    expect(await screen.findByRole("link", { name: "Set up an AI model" })).toHaveAttribute("href", "/config/wizard/add-model");
    expect(screen.getByText(/optional and off until you choose a model/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/ai\.models/);
  });
  it("tells anyone else who can turn it on", async () => {
    view(me({ admin: false }));
    expect(await screen.findByText("An administrator can turn this on.")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Set up an AI model" })).not.toBeInTheDocument();
  });
});
