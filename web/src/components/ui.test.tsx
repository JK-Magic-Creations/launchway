import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Empty, ErrorBox, StatusPill, StepStrip, stepLabel, tone } from "./ui";

describe("StatusPill", () => {
  it("shows the status in words", () => {
    render(<StatusPill status="succeeded" />);
    expect(screen.getByText("Mission complete")).toBeInTheDocument();
    expect(screen.getByText(/succeeded/)).toBeInTheDocument(); // the plain word is always there
  });
  it("shows a status a newer server introduced as it is called", () => {
    // @ts-expect-error a status this UI does not know yet
    render(<StatusPill status="paused" />);
    expect(screen.getByText("paused")).toBeInTheDocument();
  });
});

describe("tone", () => {
  it("maps statuses to colours the stylesheet knows", () => {
    expect(tone("failed")).toBe("bad");
    expect(tone("running")).toBe("run");
    expect(tone("queued")).toBe("idle");
  });
});

describe("stepLabel", () => {
  it("explains what a waiting step waits for", () => {
    expect(stepLabel({ status: "waiting", kind: "approval" })).toBe("Waiting for Clearance");
    expect(stepLabel({ status: "waiting", kind: "deploy", wait_kind: "lock", environment: "prod" })).toBe("Waiting for prod's lock");
    expect(stepLabel({ status: "failed", policy_blocked: true })).toBe("Blocked by policy");
  });
});

describe("StepStrip", () => {
  it("describes every step for assistive technology", () => {
    render(<StepStrip steps={[{ id: "build", status: "succeeded" }, { id: "test", status: "failed" }]} />);
    expect(screen.getByRole("img")).toHaveAccessibleName("build Mission complete (succeeded), test Failed");
  });
});

describe("ErrorBox", () => {
  it("is announced as an alert and shows the message", () => {
    render(<ErrorBox error={new Error("boom")} />);
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
  });
  it("renders nothing without an error", () => {
    const { container } = render(<ErrorBox error={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("Empty", () => {
  it("shows a title and its explanation", () => {
    render(<Empty title="No runs yet">Push a commit.</Empty>);
    expect(screen.getByText("No runs yet")).toBeInTheDocument();
    expect(screen.getByText("Push a commit.")).toBeInTheDocument();
  });
});
