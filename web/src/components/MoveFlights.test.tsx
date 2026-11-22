import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FlightCheck, MoveFlightsBar, useFlightSelection } from "./MoveFlights";
import { movePreview, routeFetch, squadronList } from "../test/squadronFixtures";
import { scan } from "../test/axe";

const NAMES = ["shop-api", "shop-web", "docs"];
function Harness({ onMoved }: { onMoved?: () => void }) {
  const sel = useFlightSelection(NAMES);
  return (
    <div>
      <MoveFlightsBar selection={sel} onMoved={onMoved} />
      <ul>{NAMES.map((n) => <li key={n}><FlightCheck selection={sel} name={n} /> {n}</li>)}</ul>
    </div>
  );
}
const stub = (extra: Record<string, unknown> = {}) => {
  const r = routeFetch({
    "GET /api/v1/units": squadronList(), "POST /api/v1/unit-moves/preview": movePreview(),
    "POST /api/v1/unit-moves": (b: unknown) => ((b as { homes?: unknown }).homes ? { moved: ["shop-api"], blocked: [], unchanged: [], undo: {}, to: "" } : { moved: ["shop-api"], blocked: [movePreview().plans[1]], unchanged: [], undo: { "shop-api": "" }, to: "payments" }),
    ...extra,
  });
  vi.stubGlobal("fetch", vi.fn(r.f));
  return r;
};
afterEach(() => vi.unstubAllGlobals());

describe("moving Flights", () => {
  it("selects by keyboard-reachable checkboxes, with select-all over what is in view", async () => {
    stub();
    render(<Harness />);
    const move = screen.getByRole("button", { name: /Move to Squadron/ });
    expect(move).toBeDisabled();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight shop-api" }));
    expect(move).toBeEnabled();
    expect(screen.getByText("1 Flight selected.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all Flights in view/ }));
    expect(screen.getByText("3 Flights selected.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all Flights in view/ }));
    expect(move).toBeDisabled();
  });

  it("chooses a Squadron, previews what changes per Flight, blocks one, applies the rest and can undo", async () => {
    const calls = stub();
    const moved = vi.fn();
    render(<Harness onMoved={moved} />);
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight shop-api" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight shop-web" }));
    await userEvent.click(screen.getByRole("button", { name: /Move to Squadron/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "See what would change" })).toBeDisabled();
    await userEvent.click(await within(dialog).findByRole("radio", { name: /Payments/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "See what would change" }));

    // The preview: what stops and starts, a variable that resolves differently, who gains or loses, secrets in words.
    const api = await within(dialog).findByRole("region", { name: /^shop-api/ });
    expect(within(api).getByText(/require-review/)).toBeInTheDocument();
    expect(within(api).getByText(/eu-west/)).toBeInTheDocument();
    expect(within(api).getByText(/ana gains the ability to start runs/)).toBeInTheDocument();
    expect(within(api).getByText(/bo loses the ability to start runs/)).toBeInTheDocument();
    expect(within(api).getByText("same")).toBeInTheDocument();
    const web = within(dialog).getByRole("region", { name: /^shop-web/ });
    expect(within(web).getByRole("checkbox")).toBeDisabled();
    expect(within(web).getByText(/would resolve to a different store/)).toBeInTheDocument();
    expect(within(web).getByText(/What to do:/)).toBeInTheDocument();
    expect(await scan(dialog)).toEqual([]);

    await userEvent.click(within(dialog).getByRole("button", { name: /Move 1 Flight/ }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const move = calls.calls.find((c) => c.key === "POST /api/v1/unit-moves")!;
    expect(move.body).toMatchObject({ flights: ["shop-api"], to: "payments" });
    const status = screen.getAllByRole("status").find((s) => /one audited change/.test(s.textContent ?? ""))!;
    expect(status.textContent).toMatch(/Moved 1 Flight to Payments in one audited change/);
    expect(moved).toHaveBeenCalledTimes(1);

    await userEvent.click(within(status).getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(calls.calls.filter((c) => c.key === "POST /api/v1/unit-moves")).toHaveLength(2));
    expect(calls.calls.filter((c) => c.key === "POST /api/v1/unit-moves")[1].body).toMatchObject({ flights: ["shop-api"], homes: { "shop-api": "" } });
    expect(moved).toHaveBeenCalledTimes(2);
  });

  it("closes on Escape and gives focus back to the button", async () => {
    stub();
    render(<Harness />);
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight docs" }));
    const opener = screen.getByRole("button", { name: /Move to Squadron/ });
    await userEvent.click(opener);
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("says why when the person may not move, and keeps the step disabled", async () => {
    stub({ "GET /api/v1/units": squadronList({ may_manage: false, why: "Making and changing Squadrons is for people who administer every Flight today." }) });
    render(<Harness />);
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight docs" }));
    await userEvent.click(screen.getByRole("button", { name: /Move to Squadron/ }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/administer every Flight/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "See what would change" })).toBeDisabled();
  });

  it("shows a refusal from the server in words", async () => {
    stub({ "POST /api/v1/unit-moves/preview": undefined });
    render(<Harness />);
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Flight docs" }));
    await userEvent.click(screen.getByRole("button", { name: /Move to Squadron/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("radio", { name: /Unfiled/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "See what would change" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/no route/);
  });
});
