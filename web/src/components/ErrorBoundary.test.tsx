import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { ErrorBoundary } from "./ErrorBoundary";

afterEach(cleanup);

function Bomb({ armed }: { armed: boolean }) {
  if (armed) throw new TypeError("Cannot read properties of null (reading 'length')");
  return <p>the panel</p>;
}

function Page() {
  const [armed, setArmed] = useState(true);
  return (
    <main>
      <h1>The page</h1>
      <ErrorBoundary name="The variables panel"><Bomb armed={armed} /></ErrorBoundary>
      <p>another card</p>
      <button type="button" onClick={() => setArmed(false)}>fix it</button>
    </main>
  );
}

describe("an error in one panel", () => {
  it("says what failed and keeps the rest of the page", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<Page />);
    expect(screen.getByRole("alert").textContent).toMatch(/The variables panel could not be shown.*Cannot read properties of null/);
    expect(screen.getByText("The page")).toBeTruthy();
    expect(screen.getByText("another card")).toBeTruthy();
  });
  it("draws the panel again on Try again, once the fault is gone", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "fix it" }));
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("the panel")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
  it("clears itself when the thing it shows changes", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { rerender } = render(<ErrorBoundary name="This page" resetKey="/a"><Bomb armed /></ErrorBoundary>);
    expect(screen.getByRole("alert")).toBeTruthy();
    rerender(<ErrorBoundary name="This page" resetKey="/b"><Bomb armed={false} /></ErrorBoundary>);
    expect(screen.getByText("the panel")).toBeTruthy();
  });
});
