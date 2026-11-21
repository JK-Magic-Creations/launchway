import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";
import { resetTerms } from "../terms";
import type { CacheList } from "../cacheApi";

const base: CacheList = {
  project: "shop-api", entries: [],
  totals: { entries: 0, bytes: 0, quota_bytes: 10 * 2 ** 30, max_entry_bytes: 2 ** 30, idle_days: 14 },
  policy: { enabled: true, max_project_gib: 10, max_entry_gib: 1, idle_days: 14, max_age_days: 30, source: "default" },
};
const list = vi.fn();
vi.mock("../cacheApi", async (orig) => ({ ...(await orig<typeof import("../cacheApi")>()), cacheApi: { list: (...a: unknown[]) => list(...a), deleteEntry: vi.fn(), deleteCachesNamed: vi.fn() } }));

import { ProjectCachesPage } from "./Caches";

beforeEach(() => { history.pushState(null, "", "/projects/shop-api/caches"); });
afterEach(() => { resetTerms(); list.mockReset(); });

const show = () => render(<MeContext.Provider value={me({})}><ProjectCachesPage name="shop-api" /></MeContext.Provider>);

describe("the build caches page when nothing is cached", () => {
  it("says that no step in the pipeline uses a cache, with the lines to add, and where the image layers are kept", async () => {
    list.mockResolvedValue({ ...base, use: { run: 7, steps: [], layers: [{ step: "image", registry: "harbor", repository: "team/buildkit-cache" }] } });
    show();
    await waitFor(() => screen.getByText("No step in this pipeline uses a cache"));
    expect(screen.getByText(/fallback: latest/)).toBeTruthy();
    expect(screen.getByText(/key: \{ files: \["\*\*\/go.sum"\]/)).toBeTruthy();
    expect(screen.getByText("harbor/team/buildkit-cache")).toBeTruthy();
  });
  it("says only that nothing is cached yet when a step does ask for one", async () => {
    list.mockResolvedValue({ ...base, use: { run: 7, steps: [{ step: "test", caches: ["go"] }], layers: [] } });
    show();
    await waitFor(() => screen.getByText("Nothing is cached yet"));
    expect(screen.getByText(/test \(go\)/)).toBeTruthy();
    expect(screen.queryByText("No step in this pipeline uses a cache")).toBeNull();
  });
  it("does not claim anything when the project has had no run to read", async () => {
    list.mockResolvedValue({ ...base, use: { run: 0, steps: [], layers: [] } });
    show();
    await waitFor(() => screen.getByText("Nothing is cached yet"));
  });
});
