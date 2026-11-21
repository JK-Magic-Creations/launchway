import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../App";
import { scan } from "../test/axe";

// The pages of someone who is not signed in yet: first-run setup, built-in sign-in and a one-time link, in both
// themes, scanned by axe-core. Their baseline is empty (src/a11y/baseline.json lists none of them).
function stubServer(opts: { me: { status: number; body: unknown }; info: unknown }) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const reply = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (url.includes("/api/v1/me")) return reply(opts.me.status, opts.me.body);
    if (url.includes("/api/v1/info")) return reply(200, opts.info);
    if (url.includes("/api/v1/auth/link")) return reply(200, { username: "vic", kind: "invite", expires_at: 1 });
    return reply(404, { error: "no" });
  }));
}

const CASES: { path: string; heading: RegExp; me: { status: number; body: unknown }; info: unknown }[] = [
  { path: "/", heading: /Set up this installation/, me: { status: 503, body: { error: "not set up", code: "setup_required" } }, info: { auth_mode: "local", setup_required: true } },
  { path: "/signin", heading: /Launchway/, me: { status: 401, body: { error: "sign in" } }, info: { auth_mode: "local" } },
  { path: "/signin", heading: /Launchway/, me: { status: 401, body: { error: "sign in" } }, info: { auth_mode: "oidc", local: true } },
  { path: "/accept?token=ABC", heading: /Welcome to Launchway/, me: { status: 401, body: { error: "sign in" } }, info: { auth_mode: "local" } },
];

describe.each(["light", "dark"])("signed-out pages, %s theme", (theme) => {
  beforeEach(() => document.documentElement.setAttribute("data-theme", theme));
  afterEach(() => { document.documentElement.removeAttribute("data-theme"); vi.unstubAllGlobals(); });
  it.each(CASES)("$path ($info.auth_mode) has no accessibility violations", async (c) => {
    stubServer(c);
    history.replaceState(null, "", c.path);
    const { container } = render(<App />);
    await screen.findByRole("heading", { name: c.heading }, { timeout: 8000 });
    await waitFor(() => { if (container.querySelector(".loading")) throw new Error("loading"); }, { timeout: 4000 }).catch(() => {});
    const found = await scan(document.body, { page: true });
    expect(found.map((f) => `${f.rule}: ${f.help}\n  ${f.nodes.join("\n  ")}`)).toEqual([]);
  }, 20000);
});
