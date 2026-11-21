import { useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { WordsEditor, type WordsValue } from "./WordsEditor";
import { withWords, wordsOf } from "../pages/SettingsWords";

function Host({ start, onValue }: { start: WordsValue; onValue: (v: WordsValue) => void }) {
  const [v, setV] = useState(start);
  return <WordsEditor value={v} onChange={(n) => { setV(n); onValue(n); }} />;
}
const aviation: WordsValue = { set: "aviation", overrides: {} };

describe("the words editor", () => {
  it("previews the navigation, the levels, a decision and the guide with the chosen set", async () => {
    const user = userEvent.setup();
    const seen = { v: aviation };
    render(<Host start={aviation} onValue={(n) => { seen.v = n; }} />);
    const preview = screen.getByRole("region", { name: "Preview with these words" });
    expect(within(preview).getByText("Logbook")).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(within(preview).getByText("Wingman")).toBeInTheDocument();
    expect(within(preview).getByText("Squadron")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /Plain business/ }));
    expect(seen.v.set).toBe("business");
    expect(within(preview).getByText("Audit trail")).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(within(preview).getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(within(preview).getByText("Team")).toBeInTheDocument();
    expect(within(preview).getByText("Guide")).toBeInTheDocument();
  });

  it("renames one word, shows it in the preview, and resets it", async () => {
    const user = userEvent.setup();
    const seen = { v: aviation };
    render(<Host start={aviation} onValue={(n) => { seen.v = n; }} />);
    await user.type(screen.getByLabelText("Your word for guide"), "Co-pilot");
    expect(seen.v.overrides).toEqual({ guide: "Co-pilot" });
    expect(within(screen.getByRole("region", { name: "Preview with these words" })).getByText("Co-pilot")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reset" }));
    expect(seen.v.overrides).toEqual({});
    expect(within(screen.getByRole("region", { name: "Preview with these words" })).getByText("Wingman")).toBeInTheDocument();
  });

  it("explains a word the server would refuse, and resets everything to the defaults", async () => {
    const user = userEvent.setup();
    const seen = { v: { set: "business", overrides: { guide: "x<y", unit: "Crew" } } as WordsValue };
    render(<Host start={seen.v} onValue={(n) => { seen.v = n; }} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/no < > or &/);
    await user.click(screen.getByRole("button", { name: "Reset to the defaults" }));
    expect(seen.v).toEqual(aviation);
  });
});

describe("what is stored", () => {
  it("stores nothing for the defaults and drops the older levels entry", () => {
    expect(withWords({ levels: { unit: { singular: "Group" } }, tests: {} }, aviation)).toEqual({ tests: {} });
  });
  it("stores the set and the renames, keeping the other settings", () => {
    expect(withWords({ tests: { a: 1 } }, { set: "business", overrides: { guide: "Coach" } })).toEqual({ tests: { a: 1 }, terms: { set: "business", overrides: { guide: "Coach" } } });
  });
  it("carries an older levels entry over as renames", () => {
    expect(wordsOf({ levels: { unit: { singular: "Group", plural: "Groups" } } })).toEqual({ set: "aviation", overrides: { unit: "Group", unit_plural: "Groups" } });
  });
});
