import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LogLine } from "../api";
import { LogView } from "./LogView";

const line = (seq: number, at: number, text = `line ${seq}`): LogLine => ({ seq, at, stream: "stdout", text });

describe("LogView", () => {
  it("says live, and how long it has been quiet", () => {
    render(<LogView lines={[line(1, 1_000_000)]} live empty="" now={1_000_000 + 25_000} />);
    expect(screen.getByText("live, but quiet")).toBeInTheDocument();
    expect(screen.getByText("last output 25s ago")).toBeInTheDocument();
  });
  it("is not quiet while lines keep coming", () => {
    render(<LogView lines={[line(1, 1_000_000)]} live empty="" now={1_000_000 + 1500} />);
    expect(screen.getByText("live")).toBeInTheDocument();
    expect(screen.getByText("last output just now")).toBeInTheDocument();
  });
  it("shows nothing live for a finished step", () => {
    render(<LogView lines={[line(1, 1)]} live={false} empty="" now={999} />);
    expect(screen.queryByText(/^live/)).toBeNull();
  });
  it("does not make the log itself a live region, and keeps a short status for screen readers", () => {
    render(<LogView lines={[line(1, 1), line(2, 2)]} live empty="" now={3} />);
    expect(screen.getByRole("log")).toHaveAttribute("aria-live", "off");
    expect(screen.getByRole("status")).toHaveTextContent("Live. 2 lines so far.");
  });
  it("draws only the newest lines and offers the earlier ones", () => {
    const many = Array.from({ length: 4000 }, (_, i) => line(i + 1, 1));
    const { container } = render(<LogView lines={many} live={false} empty="" />);
    expect(container.querySelectorAll(".ll").length).toBe(1500);
    fireEvent.click(screen.getByRole("button", { name: /Show 2,500 earlier lines/ }));
    expect(container.querySelectorAll(".ll").length).toBe(4000);
  });
  it("says honestly that lines are not kept in the view", () => {
    render(<LogView lines={[line(1, 1)]} live={false} empty="" dropped={80000} />);
    expect(screen.getByText(/80,000 earlier lines are not kept in this view/)).toBeInTheDocument();
    expect(screen.getByText(/all of them are in the full log/)).toBeInTheDocument();
  });
  it("pauses when the person scrolls up and jumps to the latest on request", () => {
    const { container } = render(<LogView lines={[line(1, 1), line(2, 2)]} live empty="" now={3} />);
    const box = container.querySelector(".log") as HTMLElement;
    Object.defineProperty(box, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(box, "clientHeight", { value: 200, configurable: true });
    box.scrollTop = 0;
    act(() => { fireEvent.scroll(box); });
    expect(screen.getByText(/paused while you read/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Jump to latest/ }));
    expect(screen.queryByText(/paused while you read/)).toBeNull();
  });
});
