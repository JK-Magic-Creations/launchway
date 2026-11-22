import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { FlightCheck, MoveFlightsBar, useFlightSelection } from "../components/MoveFlights";
import { installServer } from "../test/mockApi";
import { movePreview, routeFetch, squadronList } from "../test/squadronFixtures";
import { scan } from "../test/axe";

// Organize into Squadrons (the whole page), the pipelines list with its selection toolbar and callout, and the move
// dialog at each step, in both themes, scanned by axe-core with no baseline.
function Harness() {
  const sel = useFlightSelection(["shop-api", "shop-web"]);
  return <main><MoveFlightsBar selection={sel} /><ul>{["shop-api", "shop-web"].map((n) => <li key={n}><FlightCheck selection={sel} name={n} /> {n}</li>)}</ul></main>;
}

describe.each(["light", "dark"])("moving and organizing Flights, %s theme", (theme) => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  const clean = async (what: string) => {
    const found = await scan(document.body, { page: true });
    expect(found, `${what} (${theme}):\n${found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`).join("\n")}`).toEqual([]);
  };

  it("the Organize page", async () => {
    installServer();
    history.replaceState(null, "", "/units/organize");
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await screen.findByRole("heading", { name: /^shop/ }, { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await clean("organize page");
  }, 20000);

  it("the pipelines list with its selection toolbar and the Unfiled callout", async () => {
    installServer();
    history.replaceState(null, "", "/pipelines");
    const { container } = render(<App />);
    await waitFor(() => screen.getByRole("main"), { timeout: 8000 });
    await screen.findByRole("toolbar", { name: /Selected/ }, { timeout: 8000 });
    await screen.findByText(/in no (Squadron|unit)/i);
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 8000 }).catch(() => {});
    await clean("pipelines list");
  }, 20000);

  it("the move dialog: choosing, then the preview with a blocked Flight", async () => {
    const r = routeFetch({ "GET /api/v1/units": squadronList(), "POST /api/v1/unit-moves/preview": movePreview() });
    vi.stubGlobal("fetch", vi.fn(r.f));
    render(<Harness />);
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all/ }));
    await userEvent.click(screen.getByRole("button", { name: /^Move to / }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("radio", { name: /Payments/ });
    await clean("move dialog, choosing");
    await userEvent.click(within(dialog).getByRole("radio", { name: /Payments/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "See what would change" }));
    await within(dialog).findByRole("region", { name: /^shop-web/ });
    await clean("move dialog, preview");
  }, 20000);
});
