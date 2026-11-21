import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { me } from "../test/fixtures";
import { Breadcrumbs, SectionLinks, SectionTabs } from "./Navigation";
import { NO_FACTS, type Facts } from "./model";

const ctx = (f: Partial<Facts> = {}, m = me()) => ({ me: m, facts: { ...NO_FACTS, ...f } });

describe("SectionLinks", () => {
  it("shows the sections that have something, and marks the current one", () => {
    render(<SectionLinks ctx={ctx()} path="/projects/shop" />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Home", "Projects", "Deployments", "Activity", "Settings"]);
    expect(screen.getByRole("link", { name: "Projects" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Home" })).not.toHaveAttribute("aria-current");
  });
  it("adds the library when it has something, and counts approvals on Activity", () => {
    render(<SectionLinks ctx={ctx({ libraries: 1, approvals: 2 })} path="/" />);
    expect(screen.getByRole("link", { name: "Library" })).toBeInTheDocument();
    expect(within(screen.getByRole("link", { name: /Activity/ })).getByLabelText("2 waiting for a decision")).toBeInTheDocument();
  });
  it("marks a moved page's section by its new address", () => {
    render(<SectionLinks ctx={ctx()} path="/config/projects" />);
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
  });
  it("can be used with the keyboard: every item is a link with a name", () => {
    render(<SectionLinks ctx={ctx()} path="/" />);
    for (const a of screen.getAllByRole("link")) { expect(a).toHaveAttribute("href"); expect(a.textContent).not.toBe(""); }
  });
});

describe("Breadcrumbs", () => {
  it("links back up and marks the page itself", () => {
    render(<Breadcrumbs path="/projects/shop/pipeline" />);
    expect(screen.getByRole("navigation", { name: "You are here" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Projects" })).toHaveAttribute("href", "/projects");
    expect(screen.getByRole("link", { name: "shop" })).toHaveAttribute("href", "/projects/shop");
    expect(screen.getByText("Pipeline")).toHaveAttribute("aria-current", "page");
  });
  it("is not drawn on a section's own page", () => {
    const { container } = render(<Breadcrumbs path="/projects" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("SectionTabs", () => {
  it("shows the section's pages with the current one marked", () => {
    render(<SectionTabs ctx={ctx({ applications: 1 })} path="/deployments/applications/x" />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Overview", "Environments", "Applications"]);
    expect(screen.getByRole("link", { name: "Applications" })).toHaveAttribute("aria-current", "page");
  });
  it("shows none for a section with a single page", () => {
    const { container } = render(<SectionTabs ctx={ctx()} path="/projects" />);
    expect(container).toBeEmptyDOMElement();
  });
});
