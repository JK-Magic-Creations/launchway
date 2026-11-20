import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { applications } from "../guided/derive";
import { approval, environment, me, project, run, runner } from "../test/fixtures";
import { expectEntitiesLinked } from "../test/entities";
import { Home, type HomeProps } from "./Home";

const props = (over: Partial<HomeProps> = {}): HomeProps => ({
  me: me({ auth_mode: "none" }), projects: [], runs: [], runners: [], environments: [], applications: [], approvals: [], now: 10_000, ...over,
});

describe("Home on a new installation", () => {
  it("asks for the first thing and nothing else", () => {
    render(<Home {...props()} />);
    expect(screen.getByRole("heading", { name: "Add your first project" })).toBeInTheDocument();
    expect(screen.getByText("0 of 5 essentials done")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a project" })).toHaveAttribute("href", "/config/wizard/connect-repository");
    expect(screen.getByText("Nothing needs you.")).toBeInTheDocument();
    expect(screen.queryByText("Latest runs")).not.toBeInTheDocument();
  });
  it("lists the rest of the checklist as links", () => {
    render(<Home {...props()} />);
    const list = screen.getByRole("region", { name: /Setup checklist/ });
    expect(within(list).getAllByRole("link").map((a) => a.textContent)).toEqual([
      "Add your first project", "Choose who does the work", "See your first run pass", "Create an environment", "Turn on sign-in",
    ]);
  });
});

describe("Home as setup progresses", () => {
  it("folds what is done into one line and moves the card on", () => {
    render(<Home {...props({ projects: [project()], runners: [runner()] })} />);
    expect(screen.getByRole("heading", { name: "See your first run pass" })).toBeInTheDocument();
    expect(screen.getByText(/Done: Add your first project, Choose who does the work/)).toBeInTheDocument();
    expect(screen.getByText("2 of 5 essentials done")).toBeInTheDocument();
  });
  it("does not walk a person who is not an administrator through setup", () => {
    render(<Home {...props({ projects: [project()], me: me({ admin: false }) })} />);
    expect(screen.queryByText(/essentials done/)).not.toBeInTheDocument();
  });
  it("is all set when sign-in is on too", () => {
    render(<Home {...props({ projects: [project()], runners: [runner()], runs: [run()], environments: [environment()], me: me({ auth_mode: "oidc" }) })} />);
    expect(screen.getByRole("status")).toHaveTextContent("You are all set up.");
    expect(screen.queryByRole("region", { name: /Setup checklist/ })).not.toBeInTheDocument();
  });
});

describe("Home on a busy installation", () => {
  const busy = () => props({
    projects: [project({ last_run: run({ id: "run_412", number: 412, status: "failed" }) }), project({ name: "shop-web", last_run: run({ status: "succeeded" }) })],
    runs: [run({ id: "run_412", number: 412, status: "failed" }), run({ id: "run_3", number: 3, project: "shop-web" })],
    runners: [runner({ name: "edge", online: false }), runner({ id: "r2" })], environments: [environment()],
    approvals: [approval()], me: me({ auth_mode: "oidc" }),
    applications: applications([environment({ services: [{ project: "shop-api", service: "api", waiting: [], last: { id: "d", run_id: "run_1", run_number: 1, project: "shop-api", step: "d", environment: "staging", service: "api", target: "t", application: "shop-api-prod", mode: "argocd", version: "1", changed: true, phase: "degraded", detail: "", opened_at: 1, holding: false, observed_at: 5, snapshot: { app: { found: true, sync: "Synced", health: "Degraded", source: 0, automated: false } } } }] })]),
  });
  it("shows what needs attention, urgent first, each with its button", () => {
    render(<Home {...busy()} />);
    const list = screen.getByRole("region", { name: "Needs your attention" });
    const buttons = within(list).getAllByRole("link").filter((a) => a.classList.contains("btn")).map((a) => a.textContent);
    expect(buttons).toEqual(["See what went wrong", "Look into it", "Review", "Open runners"]);
  });
  it("links every name it mentions", () => {
    const { container } = render(<Home {...busy()} />);
    expectEntitiesLinked(container, ["shop-api", "shop-web", "shop-api-prod", "edge"]);
  });
  it("shows the latest three runs", () => {
    render(<Home {...busy()} />);
    expect(within(screen.getByRole("region", { name: "Latest runs" })).getAllByRole("listitem")).toHaveLength(2);
  });
});
