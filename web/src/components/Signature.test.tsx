import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SignatureBadge, useSignatures } from "./Signature";
import type { SignatureResult } from "../signingApi";

const result = (state: SignatureResult["state"], extra: Partial<SignatureResult> = {}): SignatureResult => ({ object: "abc", kind: "commit", state, explanation: `explained ${state}`, ...extra });

afterEach(() => vi.unstubAllGlobals());

describe("the signature badge", () => {
  it("says what each state is and explains it on a click", async () => {
    render(<SignatureBadge result={result("verified", { signer: "dana", signer_is_author: true })} />);
    const b = screen.getByRole("button", { name: "Verified" });
    expect(b.getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(b);
    expect(b.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText(/explained verified/).textContent).toContain("The signer is the author.");
  });
  it("shows a bad or revoked signature as a warning, and nothing for a quiet unsigned one", () => {
    const { rerender } = render(<SignatureBadge result={result("bad_signature")} />);
    expect(screen.getByRole("button", { name: "Bad signature" }).className).toContain("sig-bad");
    rerender(<SignatureBadge result={result("key_revoked")} />);
    expect(screen.getByRole("button", { name: "Key revoked" }).className).toContain("sig-bad");
    rerender(<SignatureBadge result={result("unsigned")} quiet />);
    expect(screen.queryByRole("button")).toBeNull();
    rerender(<SignatureBadge result={result("unsigned")} />);
    expect(screen.getByRole("button", { name: "Unsigned" })).toBeTruthy();
    rerender(<SignatureBadge />);
    expect(screen.queryByRole("button")).toBeNull();
  });
  it("asks the server once for the objects it is given, and shows nothing if the server has no verified commits", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (url: string) => {
      calls.push(url);
      return new Response(JSON.stringify({ items: [result("verified", { object: "abc" })] }), { status: 200 });
    }));
    function Probe() { const s = useSignatures("acme/shop", ["abc", "def"]); return <p>{Object.keys(s).join(",")}</p>; }
    render(<Probe />);
    await waitFor(() => expect(screen.getByText("abc")).toBeTruthy());
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain("/api/v1/repositories/acme/shop/signatures?shas=abc%2Cdef");
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: "off", code: "feature_off" }), { status: 404 })));
    render(<Probe />);
    expect(screen.queryAllByText("abc").length).toBe(1);
  });
});
