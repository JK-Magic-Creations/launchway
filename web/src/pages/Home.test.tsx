import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HomeAnswer } from "../organizeApi";
import { applications } from "../guided/derive";
import { approval, environment, me, project, run, runner } from "../test/fixtures";
import { expectEntitiesLinked } from "../test/entities";
import { Home, type HomeProps } from "./Home";

const props = (over: Partial<HomeProps> = {}): HomeProps => ({
  me: me({ auth_mode: "none" }), projects: [], runs: [], runners: [], environments: [], applications: [], approvals: [], now: 10_000, ...over,
});

/** The server's one Home answer, as the sections below the checklist ask for it. */
const answer = (over: Partial<HomeAnswer> = {}): HomeAnswer => ({
  view: "all", at: 1, needs: [], dismissed: [], running: [], categories: [], favorites: [], recent: [], counts: { projects: 1, needs: 0 },
  layout: { page: "home", scope: "base", shared: {}, personal: {}, effective: {}, levels: [], customized: false, may_arrange: {} }, ...over,
});
const stubHome = (a: HomeAnswer) => vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(a), { status: 200, headers: { "Content-Type": "application/json" } })));
afterEach(() => vi.unstubAllGlobals());
const failedNeed = { key: "failed:run_412", kind: "failed" as const, tone: "bad" as const, project: "shop-api", title: "Sortie #412 of shop-api failed on main", why: "It is the latest result on main, the default branch; nothing newer has run.", do: "See what went wrong, then fix it or run it again.", since: 9_000, href: "/runs/run_412" };

describe("Home on a new installation", () => {
  it("asks for the first thing and nothing else", async () => {
    stubHome(answer({ counts: { projects: 0, needs: 0 } }));
    render(<Home {...props()} />);
    expect(screen.getByRole("heading", { name: "Add your first project" })).toBeInTheDocument();
    expect(screen.getByText("0 of 5 essentials done")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a project" })).toHaveAttribute("href", "/config/wizard/connect-repository");
    expect(await screen.findByText(/Nothing needs you\./)).toBeInTheDocument();
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
  it("shows what needs attention, each saying why it is there and what to do", async () => {
    stubHome(answer({ needs: [failedNeed, { ...failedNeed, key: "decision:run_9/gate", kind: "decision", tone: "warn", title: "Waiting for a decision: Release?", why: "Sortie #9 is paused until someone says Go or No-go.", do: "Review it and decide.", href: "/runs/run_9?step=gate" }] }));
    render(<Home {...busy()} />);
    const list = (await screen.findByRole("heading", { name: /Needs you/ })).closest(".sheet") as HTMLElement;
    expect(await within(list).findByText(/latest result on main, the default branch/)).toBeInTheDocument();
    expect(within(list).getAllByText(/What to do:/)).toHaveLength(2);
    // What is not a run or a decision stays as it was: an unhealthy application, a runner that does not answer.
    const buttons = within(list).getAllByRole("link").filter((a) => a.classList.contains("btn")).map((a) => a.textContent);
    expect(buttons).toEqual(expect.arrayContaining(["Look into it", "Open runners"]));
  });
  it("does not list a failure that a newer Sortie is already retrying: it is under Running now", async () => {
    stubHome(answer({ needs: [], running: [{ id: "run_413", project: "shop-api", number: 413, branch: "main", status: "running", step: "unit-tests", started_at: 5_000, created_at: 4_000, actor: "Priya", mine: true, retry_of: 412 }] }));
    render(<Home {...busy()} />);
    expect(await screen.findByText("Retrying #412")).toBeInTheDocument();
    const need = (await screen.findByRole("heading", { name: /Needs you/ })).closest(".sheet") as HTMLElement;
    expect(within(need).queryByText(/Sortie #412/)).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Running now/ })).toBeInTheDocument();
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

describe("Home, v2", () => {
  const line = (over: Partial<import("../api/launch").LaunchLineView> = {}): import("../api/launch").LaunchLineView => ({
    run: "run_412", number: 412, project: "shop-api", message: "Round refunds per line item", actor: "Priya", ref: "refs/heads/main", sha: "a41f9c2a", status: "failed",
    created_at: 1_000, finished_at: 9_000, tone: "bad", now: "Stopped at tests. unit-tests ended with exit code 1",
    stations: [
      { key: "commit", label: "Commit", state: "done", detail: "a41f9c2a" }, { key: "checks", label: "Checks", state: "done", detail: "Passed" },
      { key: "tests", label: "Tests", state: "failed", detail: "unit-tests ended with exit code 1" }, { key: "package", label: "Package", state: "idle", detail: "Not started" },
    ], ...over,
  });
  const base = () => props({
    projects: [project({ last_run: run({ id: "run_412", number: 412, status: "failed" }) })], runs: [run({ id: "run_412", number: 412, status: "failed" })],
    runners: [runner()], environments: [environment()], me: me({ auth_mode: "oidc" }),
  });
  it("says how many things need you, by the server's rule", async () => {
    stubHome(answer({ needs: [failedNeed] }));
    render(<Home {...base()} lines={[line()]} images={2} />);
    expect(await screen.findByRole("heading", { level: 1, name: "1 thing needs you" })).toBeInTheDocument();
    const need = (await screen.findByRole("heading", { name: /Needs you/ })).closest(".sheet") as HTMLElement;
    expect(await within(need).findByRole("link", { name: /Sortie #412 of shop-api failed on main/ })).toHaveAttribute("href", "/runs/run_412");
    expect(within(need).getByRole("link", { name: "Open" })).toHaveAttribute("href", "/runs/run_412");
  });
  it("is calm when the only failure is being retried: All clear", async () => {
    stubHome(answer());
    render(<Home {...base()} lines={[line()]} images={2} />);
    expect(await screen.findByRole("heading", { level: 1, name: "All clear" })).toBeInTheDocument();
  });
  it("draws each recent change as a launch line with one sentence", () => {
    render(<Home {...base()} lines={[line()]} images={2} />);
    const sheet = screen.getByRole("heading", { name: "Changes on their way" }).closest("section")!;
    expect(within(sheet).getByRole("list", { name: /Tests failed/ })).toBeInTheDocument();
    expect(within(sheet).getByText("Stopped at tests. unit-tests ended with exit code 1")).toBeInTheDocument();
    expect(within(sheet).getByRole("link", { name: /Round refunds per line item/ })).toHaveAttribute("href", "/runs/run_412");
  });
  it("is calm when nothing needs a person: All clear", async () => {
    stubHome(answer());
    render(<Home {...props({ projects: [project({ last_run: run({ status: "succeeded" }) })], runs: [run()], runners: [runner()], environments: [environment()], me: me({ auth_mode: "oidc" }) })} />);
    expect(await screen.findByRole("heading", { level: 1, name: "All clear" })).toBeInTheDocument();
    expect((await screen.findAllByText(/Nothing needs you\./)).length).toBeGreaterThan(0);
  });
  it("shows the Explore strip with live counts from what the server holds", () => {
    render(<Home {...base()} lines={[line()]} images={7} />);
    const strip = screen.getByRole("region", { name: "Explore" });
    expect(within(strip).getByRole("link", { name: /Images/ })).toHaveTextContent("7");
    expect(within(strip).getByRole("link", { name: /Code/ })).toHaveTextContent("1project");
    expect(within(strip).getByRole("link", { name: /Pipelines/ })).toHaveTextContent("1 stopped");
    expect(within(strip).getByRole("link", { name: /Waiting/ })).toHaveTextContent("Nothing waits");
    expect(within(strip).getByRole("link", { name: /Traffic/ })).toHaveTextContent("Coming");
  });
  it("counts decisions waiting", () => {
    render(<Home {...base()} approvals={[approval(), approval({ step_id: "b", run_id: "run_9" })]} />);
    expect(within(screen.getByRole("region", { name: "Explore" })).getByRole("link", { name: /Waiting/ })).toHaveTextContent("2decisions");
  });
});
