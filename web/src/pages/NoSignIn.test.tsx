import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { signInApi } from "../signInApi";
import { NoSignInBanner } from "./NoSignIn";

describe("NoSignInBanner", () => {
  it("says there is no sign-in and gives the setup code once after the click", async () => {
    vi.spyOn(signInApi, "setupNow").mockResolvedValue({ setup_code: "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE", next: "Restart the servers." });
    render(<NoSignInBanner off={{ warning: "This installation has no sign-in: anyone can reach it.", next: "x" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("This installation has no sign-in");
    await userEvent.click(screen.getByRole("button", { name: "Set up sign-in now" }));
    expect(await screen.findByText("AAAAA-BBBBB-CCCCC-DDDDD-EEEEE")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Set up sign-in now" })).toBeNull();
  });
});
