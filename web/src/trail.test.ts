import { describe, expect, it } from "vitest";
import { entryFor } from "./trail";

describe("what the trail keeps", () => {
  it("names the object and its project, and keeps the view's address", () => {
    history.replaceState(null, "", "/runs/run_9?tab=tests&step=unit");
    expect(entryFor("/runs/run_9", "Tests · run run_9")).toMatchObject({ kind: "run", obj: "run_9", href: "/runs/run_9?tab=tests&step=unit" });
    history.replaceState(null, "", "/projects/shop-api/pipeline?view=history");
    expect(entryFor("/projects/shop-api/pipeline", "History · shop-api")).toMatchObject({ kind: "pipeline", obj: "shop-api", project: "shop-api", href: "/projects/shop-api/pipeline?view=history" });
    expect(entryFor("/repositories/acme/shop/pulls/4", "PR")).toMatchObject({ kind: "pull", obj: "acme/shop#4", project: "acme/shop" });
    expect(entryFor("/repositories/acme/shop", "shop")).toMatchObject({ kind: "repository", project: "acme/shop" });
    expect(entryFor("/settings/runners", "Runners")).toMatchObject({ kind: "settings" });
  });
  it("leaves out Home and the lists", () => {
    for (const p of ["/", "/pipelines", "/activity", "/projects", "/repositories"]) expect(entryFor(p, "x")).toBeNull();
  });
});
