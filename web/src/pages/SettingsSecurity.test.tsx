import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsSecurityPage } from "./SettingsSecurity";
import { installServer } from "../test/mockApi";
import { MeContext } from "../auth";
import { me } from "../test/fixtures";

// Settings → Security says, in plain words, whether secrets are encrypted at rest, and never shows a key.
describe("Settings → Security", () => {
  beforeEach(() => { HTMLCanvasElement.prototype.getContext = (() => null) as never; history.replaceState(null, "", "/settings/security"); });
  afterEach(() => vi.unstubAllGlobals());

  it("shows an installation that is encrypted at rest", async () => {
    installServer();
    render(<MeContext.Provider value={me()}><SettingsSecurityPage /></MeContext.Provider>);
    expect(await screen.findByText("Encrypted at rest")).toBeTruthy();
    expect(screen.getByText(/A database dump alone cannot reveal secrets/)).toBeTruthy();
    expect(screen.getByText("Yes")).toBeTruthy();
    expect(screen.getByText(/Not yet encrypted by Launchway: artifacts, logs/)).toBeTruthy();
  });

  it("says loudly that an installation without a root key is not encrypted", async () => {
    installServer({
      "/api/v1/encryption": { encrypted: false, dump_alone_cannot_reveal_secrets: false, columns: [], secrets_sealed: 0, secrets_remaining: 3, migration: { state: "not started" }, stored_addresses_with_credentials: 0,
        warnings: ["NOT ENCRYPTED AT REST: this server has no root key."], advice: [], not_yet_encrypted: ["logs"], summary: "NOT ENCRYPTED AT REST: this server has no root key, so its signing keys are stored in the clear." },
    });
    render(<MeContext.Provider value={me()}><SettingsSecurityPage /></MeContext.Provider>);
    expect(await screen.findByText("NOT encrypted at rest")).toBeTruthy();
    expect(screen.getByText("No")).toBeTruthy();
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0);
  });

  it("is for administrators", async () => {
    installServer({}, me({ admin: false }));
    render(<MeContext.Provider value={me({ admin: false })}><SettingsSecurityPage /></MeContext.Provider>);
    expect(await screen.findByText(/Only an administrator can see how this server's secrets are protected/)).toBeTruthy();
  });
});
