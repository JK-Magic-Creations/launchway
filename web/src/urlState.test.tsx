import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { bool, int, oneOf, readUrl, setUrl, useUrlState } from "./urlState";

function Probe() {
  const [tab, setTab] = useUrlState("tab", "overview", oneOf("overview", "tests", "history"));
  const [q, setQ] = useUrlState("q", "");
  const [n, setN] = useUrlState("n", 1, int(1, 50));
  const [f, setF] = useUrlState("fav", false, bool);
  return (
    <div>
      <p data-testid="v">{tab}|{q}|{n}|{String(f)}</p>
      <button onClick={() => setTab("tests", "push")}>tests</button>
      <button onClick={() => setQ("pay")}>type</button>
      <button onClick={() => { setTab("overview", "push"); setN(1); setF(false); }}>defaults</button>
      <button onClick={() => { setN(7); setF(true); }}>more</button>
    </div>
  );
}

afterEach(() => history.replaceState(null, "", "/"));

describe("useUrlState", () => {
  it("keeps defaults out of the address and reads values back", () => {
    history.replaceState(null, "", "/x");
    render(<Probe />);
    act(() => screen.getByText("tests").click());
    expect(location.search).toBe("?tab=tests");
    act(() => screen.getByText("type").click());
    expect(location.search).toBe("?tab=tests&q=pay");
    act(() => screen.getByText("more").click());
    expect(location.search).toBe("?tab=tests&q=pay&n=7&fav=1");
    act(() => screen.getByText("defaults").click());
    expect(location.search).toBe("?q=pay");
    expect(screen.getByTestId("v").textContent).toBe("overview|pay|1|false");
  });
  it("falls back to the default for a value that does not parse, and never throws", () => {
    history.replaceState(null, "", "/x?tab=%E0%A4%A&n=abc&fav=maybe");
    render(<Probe />);
    expect(screen.getByTestId("v").textContent).toBe("overview||1|false");
  });
  it("push makes Back return to the view before; replace does not", () => {
    history.replaceState(null, "", "/x");
    const before = history.length;
    render(<Probe />);
    act(() => screen.getByText("type").click());
    act(() => screen.getByText("type").click());
    expect(history.length).toBe(before);
    act(() => screen.getByText("tests").click());
    expect(history.length).toBe(before + 1);
  });
  it("reads and sets outside a component", () => {
    history.replaceState(null, "", "/x?tab=history");
    expect(readUrl("tab", "overview", oneOf("overview", "history"))).toBe("history");
    setUrl({ tab: null, node: "payments/api" });
    expect(location.search).toBe("?node=payments/api");
  });
});
