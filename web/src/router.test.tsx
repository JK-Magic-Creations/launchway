import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Link } from "./components/ui";
import { match } from "./router";

describe("match", () => {
  it("reads the parameters of a path", () => {
    expect(match("/runs/:id", "/runs/run_x")).toEqual({ id: "run_x" });
    expect(match("/p/:a/r/:b", "/p/one/r/two%20x")).toEqual({ a: "one", b: "two x" });
  });
  it("does not match a different path", () => {
    expect(match("/runs/:id", "/projects/x")).toBeNull();
    expect(match("/runs/:id", "/runs")).toBeNull();
  });
});

describe("Link", () => {
  it("navigates without reloading on a plain click", () => {
    render(<Link href="/runs/abc">Run</Link>);
    fireEvent.click(screen.getByRole("link", { name: "Run" }));
    expect(location.pathname).toBe("/runs/abc");
  });
  it("leaves a ctrl-click to the browser", () => {
    history.pushState(null, "", "/start");
    render(<Link href="/runs/abc">Run</Link>);
    // jsdom cannot open a tab: record that the browser's default was left alone.
    let left = false;
    document.addEventListener("click", (e) => { left = !e.defaultPrevented; e.preventDefault(); }, { once: true });
    fireEvent.click(screen.getByRole("link", { name: "Run" }), { ctrlKey: true });
    expect(left).toBe(true);
    expect(location.pathname).toBe("/start");
  });
});
