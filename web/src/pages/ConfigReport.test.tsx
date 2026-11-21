import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfigReportPage } from "./ConfigReport";
import { ProjectConfigurationPage } from "./Configuration";
import { scan } from "../test/axe";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";
import { installServer } from "../test/mockApi";
import type { AnsibleDocument, ConfigReport } from "../configCodeApi";

const row = { id: "cac_1", run_id: "run_7", step: "configure", attempt: 1, project: "shop-api", environment: "prod", kind: "ansible" as const, commit: "0123456789abcdef", check: false, status: "failed", recap: "2 hosts: 1 changed, 0 ok, 1 failed", created_at: Date.now(), run_number: 7 };
const task = (name: string, results: AnsibleDocument["plays"][0]["tasks"][0]["results"]) => ({ name, action: "ansible.builtin.copy", results });
const ansible: ConfigReport<AnsibleDocument> = {
  report: row,
  summary: { failed_hosts: ["web2"], drift: null },
  document: {
    check: false, diff: true, duration_ms: 9000, playbook: "site.yml",
    summary: { hosts: 2, changed: 1, ok: 0, failed: 1, unreachable: 0, recap: "2 hosts: 1 changed, 0 ok, 1 failed" },
    failure: { play: "Configure the demo service", task: "Fail on the host asked to fail", host: "web2", status: "failed", msg: "this host was told to fail (web2)" },
    hosts: [
      { host: "web1", outcome: "changed", changed_tasks: ["Install the demo configuration file"], ok: 3, changed: 1, failures: 0, unreachable: 0, skipped: 0 },
      { host: "web2", outcome: "failed", ok: 3, changed: 0, failures: 1, unreachable: 0, skipped: 0 },
    ],
    plays: [{ name: "Configure the demo service", tasks: [
      task("Install the demo configuration file", { web1: { status: "changed", changed: true, diff: [{ text: "--- before\n+++ /etc/lw-demo.conf\n+message=hello\n" }] }, web2: { status: "ok" } }),
      task("Fail on the host asked to fail", { web1: { status: "ok" }, web2: { status: "failed", msg: "this host was told to fail (web2)" } }),
    ] }],
  },
};

function serve(rep: unknown) {
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") return new Response(JSON.stringify({ run: { id: "run_9", number: 9 }, hosts: ["web2"] }), { status: 201, headers: { "Content-Type": "application/json" } });
    if (String(url).includes("/me")) return new Response(JSON.stringify({ user: "ada", groups: [], auth_mode: "oidc", via: "session", admin: true, runners: true, roles: { "shop-api": "operator" }, mcp: { enabled: false, path: "/mcp" } }), { status: 200, headers: { "Content-Type": "application/json" } });
    return new Response(JSON.stringify(rep), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
}

describe.each(["light", "dark"])("the configuration report page, %s theme", (theme) => {
  beforeEach(() => { document.documentElement.setAttribute("data-theme", theme); });
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });

  it("shows the failed task first, the hosts by plays, what changed and the diff, and has no accessibility violations", async () => {
    serve(ansible);
    render(<main><ConfigReportPage run="run_7" step="configure" /></main>);
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed web2");
    expect(screen.getByRole("alert")).toHaveTextContent("this host was told to fail");
    const matrix = screen.getByRole("table", { name: /Hosts by plays/ });
    expect(within(matrix).getByRole("row", { name: /web1.*changed/ })).toBeTruthy();
    expect(screen.getByText("2 hosts: 1 changed, 0 ok, 1 failed")).toBeTruthy();
    expect(screen.getByText(/Install the demo configuration file on web1/)).toBeTruthy();
    expect(await scan(document.body, { page: true })).toEqual([]);
  });

  it("filters by host and by outcome", async () => {
    serve(ansible);
    render(<main><ConfigReportPage run="run_7" step="configure" /></main>);
    await screen.findByRole("alert");
    fireEvent.change(screen.getByLabelText("Outcome"), { target: { value: "failed" } });
    const matrix = screen.getByRole("table", { name: /Hosts by plays/ });
    expect(within(matrix).queryByRole("row", { name: /web1/ })).toBeNull();
    expect(within(matrix).getByRole("row", { name: /web2/ })).toBeTruthy();
  });

  it("offers to run again on the failed hosts, and does it", async () => {
    serve(ansible);
    render(<MeContext.Provider value={me({ roles: { "shop-api": "operator" } })}><main><ConfigReportPage run="run_7" step="configure" /></main></MeContext.Provider>);
    const button = await screen.findByRole("button", { name: /Run again on the failed host \(web2\)/ });
    fireEvent.click(button);
    await waitFor(() => expect(screen.getByText(/Started run #9 on web2/)).toBeTruthy());
  });

  it("says what a check-mode run would change, and offers no re-run of a check", async () => {
    const check: ConfigReport<AnsibleDocument> = { ...ansible, report: { ...row, check: true, status: "drifted" },
      summary: { failed_hosts: [], drift: { drifted: 1, hosts: 2, clean: false, text: "1 host drifted: Install the demo configuration file", items: [{ host: "web1", tasks: ["Install the demo configuration file"] }] } },
      document: { ...ansible.document, check: true, failure: undefined } };
    serve(check);
    render(<main><ConfigReportPage run="run_7" step="configure" /></main>);
    expect(await screen.findByText("1 host drifted: Install the demo configuration file")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Run again/ })).toBeNull();
    expect(screen.getByText("What would change")).toBeTruthy();
  });

  it("shows a plan with the destroyed resources first and sensitive values hidden", async () => {
    serve({ report: { ...row, kind: "plan", status: "planned", recap: "1 to add, 0 to change, 1 to destroy" }, summary: {},
      document: { plan: { summary: "1 to add, 0 to change, 1 to destroy", counts: { add: 1, change: 0, destroy: 1, replace: 0 },
        risk: { level: "high", destroyed: ["random_pet.server[1]"], notes: ["1 resource(s) will be destroyed: the data in them is gone"] }, outputs: [],
        resources: [{ address: "random_pet.server[1]", type: "random_pet", action: "delete", attrs: [{ path: "id", before: "web-x" }] }, { address: "random_password.db", type: "random_password", action: "create", attrs: [{ path: "result", sensitive: true }] }] }, meta: {} } });
    render(<main><ConfigReportPage run="run_7" step="plan" /></main>);
    expect(await screen.findByText("Risk: high")).toBeTruthy();
    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("random_pet.server[1]");
    expect(screen.getByText("(sensitive, hidden)")).toBeTruthy();
    expect(await scan(document.body, { page: true })).toEqual([]);
  });
});

describe("the configuration page of a project", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("shows each Airfield's last apply, drift and plan, and the state", async () => {
    installServer({ "/api/v1/projects/shop-api/configuration": { sealed: true,
      environments: [{ name: "prod", ansible: row, drift: { ...row, check: true, kind: "ansible", recap: "2 hosts: 1 would change, 1 ok, 0 failed" } }],
      state: [{ name: "prod", version: 3, serial: 7 }] } });
    render(<main><ProjectConfigurationPage name="shop-api" /></main>);
    expect(await screen.findByRole("link", { name: /2 hosts: 1 would change/ })).toBeTruthy();
    expect(screen.getByRole("table", { name: /State workspaces/ })).toBeTruthy();
  });
});
