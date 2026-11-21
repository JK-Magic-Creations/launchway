import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { InstallationProvider } from "../guided/facts";
import { me } from "../test/fixtures";

vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, projects: vi.fn().mockResolvedValue([]), runs: vi.fn().mockResolvedValue([]), runners: vi.fn().mockResolvedValue([]), environments: vi.fn().mockResolvedValue([]),
    images: vi.fn(), runImages: vi.fn() } };
});
vi.mock("../configApi", async (orig) => ({ ...(await orig<typeof import("../configApi")>()), configApi: { libraries: vi.fn().mockResolvedValue([]) } }));

import { api } from "../api";
import { ImageDetailPage } from "./ImageDetail";

const D = "sha256:" + "a".repeat(64);
function show() {
  const m = me();
  render(<MeContext.Provider value={m}><InstallationProvider me={m} approvals={[]}><ImageDetailPage digest={D} /></InstallationProvider></MeContext.Provider>);
}
beforeEach(() => { history.pushState(null, "", "/images/digest/" + D); });

describe("the image page", () => {
  it("says which run built it and shows findings by severity", async () => {
    vi.mocked(api.images).mockResolvedValue({ hits: [{ project: "shop-api", run_id: "run_1", run_number: 4, ref: "refs/heads/main", step: "image", attempt: 1, kind: "index", repository: "reg.example/shop", digest: D, pushed: true, signed: true, outcome: "succeeded", created_at: 1 }] });
    vi.mocked(api.runImages).mockResolvedValue({ images: [{ step: "image", attempt: 1, outcome: "succeeded", created_at: 1, digest: D, size: 1, pushed: true, signed: true, sbom: true, sbom_artifact: "sbom", scan: { critical: 1, high: 2, medium: 0, low: 4, unknown: 0 } }] });
    show();
    expect(await screen.findByText("3 critical or high findings.")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "run #4" })).toHaveAttribute("href", "/runs/run_1");
  });
  it("is honest about a digest no run built", async () => {
    vi.mocked(api.images).mockResolvedValue({ hits: [] });
    show();
    expect(await screen.findByText("No run built this digest")).toBeInTheDocument();
  });
});
