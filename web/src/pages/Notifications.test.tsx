import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { InboxPage, NotificationSettingsPage } from "./Notifications";

const prefs = (mailOn: boolean) => ({
  kinds: [
    { kind: "review_requested", title: "A review is asked of you", default: "email", channel: "email", waiting_on_you: true },
    { kind: "comment", title: "A comment on a pull request or work item you take part in", default: "in_app", channel: "in_app", waiting_on_you: false },
  ],
  watches: [{ scope: "repo:acme/shop", level: "all" }], settings: { quiet_start: -1, quiet_end: -1, zone: "UTC", digest: false, digest_hour: 8 },
  mail_on: mailOn, address_known: true, channels: ["in_app", "email", "off"], levels: ["all", "participating", "none"],
});

function serve(routes: Record<string, unknown>) {
  const calls: { method: string; url: string; body?: string }[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ method: init?.method ?? "GET", url, body: init?.body as string | undefined });
    const key = `${init?.method ?? "GET"} ${url.split("?")[0]}`;
    if (!(key in routes)) return new Response(JSON.stringify({ error: "no" }), { status: 403 });
    const v = routes[key];
    return new Response(JSON.stringify(typeof v === "function" ? (v as (b: unknown) => unknown)(init?.body ? JSON.parse(String(init.body)) : undefined) : v), { status: 200 });
  }));
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("the inbox", () => {
  const items = [
    { id: "ntc_1", source: "notice", kind: "review_requested", title: "Review requested: #7 Rework checkout", repository: "acme/shop", link: "/repositories/acme/shop/pulls/7", at: "2026-11-22T10:00:00Z", read: false, done: false },
    { id: "pull:acme/shop#9", source: "pull", kind: "pull_review_requested", title: "#9 Fix the price", at: "2026-11-22T09:00:00Z", read: false, done: false, waiting_on_you: true },
  ];
  it("lists entries from every source, marks a notice read or done, and cannot mark one that waits on you", async () => {
    const calls = serve({ "GET /api/v1/inbox": { items, unread: 1, waiting: 1 }, "PUT /api/v1/inbox/read": { changed: 1 }, "PUT /api/v1/inbox/done": { changed: 1 } });
    render(<InboxPage />);
    expect((await screen.findAllByText(/Review requested: #7 Rework checkout/)).length).toBeGreaterThan(0);
    expect(screen.getByText("waiting on you")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("1 unread, 1 waiting on you");
    const buttons = screen.getAllByRole("button", { name: /Mark read|Done/ });
    expect(buttons).toHaveLength(2); // the waiting pull request has none
    await userEvent.click(screen.getByRole("button", { name: /Mark read: Review requested/ }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT" && c.url.endsWith("/inbox/read"))).toBe(true));
    expect(JSON.parse(calls.find((c) => c.url.endsWith("/inbox/read"))!.body!)).toEqual({ ids: ["ntc_1"], value: true });
    await userEvent.click(screen.getByRole("button", { name: /Done: Review requested/ }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith("/inbox/done"))).toBe(true));
  });
  it("says so when there is nothing", async () => {
    serve({ "GET /api/v1/inbox": { items: [], unread: 0, waiting: 0 } });
    render(<InboxPage />);
    expect(await screen.findByText("Nothing here")).toBeTruthy();
  });
});

describe("notification settings", () => {
  it("saves a choice at once, and offers e-mail only where the installation sends it", async () => {
    const calls = serve({ "GET /api/v1/notices/preferences": prefs(false), "GET /api/v1/mail": { enabled: false }, "PUT /api/v1/notices/preferences": prefs(false) });
    render(<NotificationSettingsPage />);
    expect(await screen.findByText(/This installation sends no e-mail/)).toBeTruthy();
    const sel = screen.getByLabelText(/How you are told: A comment on a pull request/) as HTMLSelectElement;
    expect((within(sel).getByRole("option", { name: "Inbox and e-mail" }) as HTMLOptionElement).disabled).toBe(true);
    await userEvent.selectOptions(sel, "off");
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "PUT")!.body!)).toEqual({ kinds: { comment: "off" } });
  });
  it("saves a watch level and shows the administrator's mail panel with a working test button", async () => {
    const calls = serve({ "GET /api/v1/notices/preferences": prefs(true), "PUT /api/v1/notices/preferences": prefs(true),
      "GET /api/v1/mail": { enabled: true, host: "smtp.example.com", port: 587, security: "starttls", from: "Launchway <n@example.com>", public_url: "https://l.example.com", credentials: true, include_private_details: false, pending: 0, sent: 3, failed: 0, sent_last_hour: 1, rate_per_minute: 30, rate_per_hour: 300 },
      "POST /api/v1/mail/test": { sent: true, to: "me@example.com" } });
    render(<NotificationSettingsPage />);
    await screen.findByText("Mail server (administrators)");
    await userEvent.selectOptions(screen.getByLabelText("Level for repo:acme/shop"), "none");
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "PUT")!.body!)).toEqual({ watches: [{ scope: "repo:acme/shop", level: "none" }] });
    await userEvent.click(screen.getByRole("button", { name: "Send a test message to me" }));
    expect(await screen.findByText("Sent a test message to me@example.com.")).toBeTruthy();
  });
});
