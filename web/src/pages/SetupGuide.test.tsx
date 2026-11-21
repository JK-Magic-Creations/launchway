import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { guideFixture } from "../test/mockApi";
import { SetupGuidePage } from "./SetupGuide";

type Handler = (body: Record<string, unknown>) => unknown;

/** A server for the guide: answers by "METHOD path"; anything not listed is an empty object. */
function serve(handlers: Record<string, Handler | unknown> = {}) {
  const calls: { key: string; body: Record<string, unknown> }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${String(url).split("?")[0]}`;
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    calls.push({ key, body });
    const h = handlers[key];
    const out = typeof h === "function" ? (h as Handler)(body) : h !== undefined ? h : defaults[key] ?? {};
    if (out instanceof Response) return out;
    return new Response(JSON.stringify(out), { status: 200 });
  }));
  return calls;
}
const defaults: Record<string, unknown> = {
  "GET /api/v1/setup-guide": guideFixture(),
  "GET /api/v1/signin/test": { tested: false },
  "GET /api/v1/encryption/root-key-copy": { taken: false, can_take: true },
};
const fail = (status: number, body: object) => new Response(JSON.stringify(body), { status });

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); history.replaceState(null, "", "/"); });

async function open(step?: string) {
  history.replaceState(null, "", step ? `/setup/guide?step=${step}` : "/setup/guide");
  render(<SetupGuidePage />);
  await screen.findByRole("navigation", { name: "Setup steps" });
}

describe("the setup guide", () => {
  it("lists the steps with their state and counts what is done", async () => {
    serve();
    await open();
    expect(screen.getByText(/1 of 7 steps are done/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /A runner.*done/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Bring in your repositories.*not available yet/ })).toBeTruthy();
  });

  it("offers no import button where importing cannot work, and says why and what would help", async () => {
    serve();
    await open("import");
    expect(screen.getByText(/Not available here/)).toBeTruthy();
    expect(screen.getAllByText(/Git hosting is off/).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /Find the repositories/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Add server/ })).toBeNull();
  });

  it("skipping a step is remembered, and it can be taken up again", async () => {
    serve();
    await open("encryption");
    fireEvent.click(screen.getByRole("button", { name: "Skip for now" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /Encryption and the root key.*skipped/ })).toBeTruthy());
    expect(JSON.parse(localStorage.getItem("launchway.guide.skipped") ?? "[]")).toContain("encryption");
    fireEvent.click(screen.getByRole("button", { name: /Encryption and the root key/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Take this step up again" }));
    expect(JSON.parse(localStorage.getItem("launchway.guide.skipped") ?? "[]")).not.toContain("encryption");
  });

  it("shows the helper's words (Wingman) and a Learn link on each step", async () => {
    serve();
    await open("runner");
    expect(screen.getByLabelText("Wingman")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Learn more about runners/ })).toBeTruthy();
  });
});

describe("the recovery copy of the root key", () => {
  it("asks for the passphrase twice and does not send two that differ", async () => {
    const calls = serve();
    await open("encryption");
    fireEvent.click(screen.getByRole("button", { name: /Download a recovery copy/ }));
    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "purple walrus drinks coffee" } });
    fireEvent.change(screen.getByLabelText(/The code from your authenticator/), { target: { value: "123456" } });
    fireEvent.change(screen.getByLabelText(/A passphrase for the copy/), { target: { value: "a long passphrase here" } });
    fireEvent.change(screen.getByLabelText("The passphrase again"), { target: { value: "another passphrase there" } });
    expect(await screen.findByText("The two passphrases are not the same.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Download the copy" }) as HTMLButtonElement).disabled).toBe(true);
    expect(calls.some((c) => c.key === "POST /api/v1/encryption/root-key-copy")).toBe(false);
  });

  it("sends the guarded request and says only that a copy was made, never shows a key", async () => {
    URL.createObjectURL = vi.fn(() => "blob:x") as never;
    URL.revokeObjectURL = vi.fn() as never;
    const calls = serve({ "POST /api/v1/encryption/root-key-copy": { format: "launchway-root-key-copy-v1", ciphertext: "SECRETBYTES" } });
    await open("encryption");
    fireEvent.click(screen.getByRole("button", { name: /Download a recovery copy/ }));
    fireEvent.change(screen.getByLabelText("Your password"), { target: { value: "purple walrus drinks coffee" } });
    fireEvent.change(screen.getByLabelText(/The code from your authenticator/), { target: { value: "123456" } });
    fireEvent.change(screen.getByLabelText(/A passphrase for the copy/), { target: { value: "a long passphrase here" } });
    fireEvent.change(screen.getByLabelText("The passphrase again"), { target: { value: "a long passphrase here" } });
    fireEvent.click(screen.getByRole("button", { name: "Download the copy" }));
    expect(await screen.findByText(/The copy was downloaded as launchway-root-key-copy.json/)).toBeTruthy();
    const sent = calls.find((c) => c.key === "POST /api/v1/encryption/root-key-copy")!.body;
    expect(sent).toMatchObject({ current_password: "purple walrus drinks coffee", code: "123456", passphrase: "a long passphrase here", passphrase_again: "a long passphrase here" });
    expect(document.body.textContent).not.toContain("SECRETBYTES");
  });

  it("offers nothing when the server says it cannot be done, and says why", async () => {
    const g = guideFixture();
    g.steps[1].actions = [{ name: "download_root_key_copy", available: false, who: "admin", reason: "This server has no root key configured.", would_help: "Turn on encryption at rest first." } as never];
    serve({ "GET /api/v1/setup-guide": g });
    await open("encryption");
    expect(screen.queryByRole("button", { name: /Download a recovery copy/ })).toBeNull();
    expect(screen.getByText(/no root key configured/)).toBeTruthy();
  });
});

describe("importing from another Git server", () => {
  const importable = () => {
    const g = guideFixture();
    const imp = g.steps.find((s) => s.id === "import")!;
    imp.state = "todo";
    imp.actions = [{ name: "add_connection", available: true, who: "admin" }, { name: "paste_token", available: true, who: "admin" }, { name: "import", available: true, who: "admin" }] as never;
    return g;
  };
  const item = (o: object) => ({ source: "acme/web", repository: "acme/web", url: "u", visibility: "private", action: "create", state: "planned", ...o });

  it("lists what was found, imports the ones picked, and shows each one's verification", async () => {
    const calls = serve({
      "GET /api/v1/setup-guide": importable(),
      "GET /api/v1/connections": { items: [{ name: "oldgit", kind: "gitea", base_url: "https://git.example.com", enabled: true }] },
      "POST /api/v1/imports": (b: Record<string, unknown>) => b.dry_run
        ? { dry_run: true, summary: {}, items: [item({}), item({ source: "acme/api", repository: "acme/api" }), item({ source: "acme/mine", action: "skip", state: "skipped", reason: "exists and is not a mirror" })] }
        : { dry_run: false, summary: {}, items: [item({ source: b.repository, state: "verified", source_refs: 7, hosted_refs: 7, verified: true })] },
    });
    await open("import");
    fireEvent.click(await screen.findByRole("button", { name: "Find the repositories" }));
    expect(await screen.findByText("3 repositories found")).toBeTruthy();
    expect(screen.getByText(/left alone: exists and is not a mirror/)).toBeTruthy();
    fireEvent.click(screen.getByLabelText("Import acme/api")); // deselect one
    fireEvent.click(screen.getByRole("button", { name: "Import 1 repository" }));
    expect(await screen.findByText("verified: 7 of 7 refs equal")).toBeTruthy();
    await screen.findByText(/were compared with their source, ref by ref/);
    const imported = calls.filter((c) => c.key === "POST /api/v1/imports" && !c.body.dry_run).map((c) => c.body.repository);
    expect(imported).toEqual(["acme/web"]);
  });

  it("retries the ones that failed", async () => {
    let attempt = 0;
    serve({
      "GET /api/v1/setup-guide": importable(),
      "GET /api/v1/connections": { items: [{ name: "oldgit", kind: "gitea", base_url: "https://git.example.com", enabled: true }] },
      "POST /api/v1/imports": (b: Record<string, unknown>) => {
        if (b.dry_run) return { dry_run: true, summary: {}, items: [item({})] };
        attempt++;
        return attempt === 1 ? { items: [item({ state: "failed", error: "the other server did not answer" })] } : { items: [item({ state: "verified", source_refs: 3, hosted_refs: 3, verified: true })] };
      },
    });
    await open("import");
    fireEvent.click(await screen.findByRole("button", { name: "Find the repositories" }));
    fireEvent.click(await screen.findByRole("button", { name: "Import 1 repository" }));
    expect(await screen.findByText(/the other server did not answer/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try the 1 that failed again" }));
    expect(await screen.findByText("verified: 3 of 3 refs equal")).toBeTruthy();
  });

  it("pastes a token once, sends it to the guarded call and clears it", async () => {
    const calls = serve({ "GET /api/v1/setup-guide": importable(), "GET /api/v1/connections": { items: [] }, "POST /api/v1/connections/token": { name: "oldgit", kind: "gitea" } });
    await open("import");
    fireEvent.change(await screen.findByLabelText("A name for it"), { target: { value: "oldgit" } });
    fireEvent.change(screen.getByLabelText("Its address"), { target: { value: "https://git.example.com" } });
    fireEvent.change(screen.getByLabelText("The token"), { target: { value: "tok-abc-123" } });
    fireEvent.click(screen.getByRole("button", { name: "Add server" }));
    await waitFor(() => expect(calls.some((c) => c.key === "POST /api/v1/connections/token")).toBe(true));
    expect(calls.find((c) => c.key === "POST /api/v1/connections/token")!.body).toMatchObject({ name: "oldgit", token: "tok-abc-123" });
    await waitFor(() => expect((screen.getByLabelText("A name for it") as HTMLInputElement).value).toBe(""));
    expect(document.body.textContent).not.toContain("tok-abc-123");
  });

  it("does not offer pasting where the server cannot keep a token, and offers the variable instead", async () => {
    const g = importable();
    g.steps.find((s) => s.id === "import")!.actions[1] = { name: "paste_token", available: false, who: "admin", reason: "This server runs in a Kubernetes pod with no disk of its own." } as never;
    serve({ "GET /api/v1/setup-guide": g, "GET /api/v1/connections": { items: [] } });
    await open("import");
    expect(await screen.findByText(/no disk of its own/)).toBeTruthy();
    expect((screen.getByLabelText(/Paste it now/) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByLabelText("Variable name")).toBeTruthy();
  });
});

describe("testing an identity provider before anything is switched", () => {
  it("opens a second window, waits for the result and then shows what to set, with the switch itself marked unavailable", async () => {
    const open_ = vi.fn(() => ({}) as Window);
    vi.stubGlobal("open", open_);
    const started = Date.now();
    let polls = 0;
    serve({
      "POST /api/v1/signin/test": { url: "https://sso.example.com/authorize?x=1", redirect_url: location.origin + "/auth/signin-test", expires_in_seconds: 600 },
      "GET /api/v1/signin/test": () => (++polls < 2 ? { tested: false } : { tested: true, ok: true, at: started + 5000, detail: "signed in at https://sso.example.com as ada; groups: staff" }),
      "POST /api/v1/config/check/signin": { ok: true, problems: [], endpoints: {}, snippet: "auth:\n  mode: oidc\n" },
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Connect an identity provider" }));
    fireEvent.change(await screen.findByLabelText("The provider's address (issuer)"), { target: { value: "https://sso.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Test the sign-in in a second window" }));
    await waitFor(() => expect(open_).toHaveBeenCalledWith("https://sso.example.com/authorize?x=1", "launchway-signin-test"));
    expect(await screen.findByText(/Waiting for you to sign in at the provider/)).toBeTruthy();
    expect(await screen.findByText(/The test worked\./, undefined, { timeout: 8000 })).toBeTruthy();
    expect(await screen.findByText(/mode: oidc/)).toBeTruthy();
    expect(screen.getByText(/Switching the provider on is a setting in the server's own file/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /switch .* on/i })).toBeNull();
  }, 15000);

  it("shows the server's reason when the test cannot start", async () => {
    vi.stubGlobal("open", vi.fn());
    serve({ "POST /api/v1/signin/test": fail(409, { error: "the environment variable LAUNCHWAY_OIDC_SECRET is not set on this server", code: "secret_missing", next: "Put the secret there." }) });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Connect an identity provider" }));
    fireEvent.change(await screen.findByLabelText("The provider's address (issuer)"), { target: { value: "https://sso.example.com" } });
    fireEvent.click(screen.getByRole("button", { name: "Test the sign-in in a second window" }));
    expect(await screen.findByText(/LAUNCHWAY_OIDC_SECRET is not set on this server/)).toBeTruthy();
  });
});

describe("the finish page", () => {
  it("shows the security checklist, saying what needs attention", async () => {
    serve();
    await open("finish");
    expect(screen.getByText(/1 thing needs your attention/)).toBeTruthy();
    expect(screen.getByText("Traffic is encrypted in transit")).toBeTruthy();
    expect(screen.getAllByText("needs attention").length).toBe(1);
  });
});
