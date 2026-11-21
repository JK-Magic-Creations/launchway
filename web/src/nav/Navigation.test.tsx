import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { me } from "../test/fixtures";
import { Breadcrumbs } from "./Navigation";
import { NO_FACTS, type Facts } from "./model";
import { Route, TabBar } from "../shell/Route";
import { SideNav } from "../shell/SideNav";

const ctx = (f: Partial<Facts> = {}, m = me()) => ({ me: m, facts: { ...NO_FACTS, ...f } });

describe("the top route", () => {
  it("shows the ten areas, in order, with the current one marked", () => {
    render(<Route ctx={ctx()} on="projects" needs={0} />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(
      ["Tower", "Code", "Pipelines", "Images", "Launches", "Radar", "Changes", "Activity", "Library", "Settings"]);
    expect(screen.getByRole("link", { name: "Code" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Tower" })).not.toHaveAttribute("aria-current");
  });
  it("describes each area on hover and focus, and counts what needs a person on Home", () => {
    render(<Route ctx={ctx()} on="home" needs={3} />);
    expect(screen.getByRole("link", { name: /Radar/ })).toHaveAttribute("data-tip", "How requests flow between services, and what protects and watches them.");
    expect(screen.getByLabelText("3 things need you")).toBeInTheDocument();
  });
  it("separates the groups with a divider that screen readers skip", () => {
    const { container } = render(<Route ctx={ctx()} on="home" needs={0} />);
    const seps = container.querySelectorAll(".sep");
    expect(seps).toHaveLength(3);
    seps.forEach((s) => expect(s).toHaveAttribute("aria-hidden", "true"));
  });
  it("can be used with the keyboard: every area is a link with a name", () => {
    render(<Route ctx={ctx()} on="home" needs={0} />);
    for (const a of screen.getAllByRole("link")) { expect(a).toHaveAttribute("href"); expect(a.textContent).not.toBe(""); }
  });
});

describe("the phone's bottom bar", () => {
  it("has five areas and All areas", () => {
    render(<TabBar ctx={ctx()} on="home" needs={0} onAreas={() => {}} />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Tower", "Code", "Launches", "Radar", "Changes"]);
    expect(screen.getByRole("button", { name: "All areas" })).toBeInTheDocument();
  });
});

describe("the left column", () => {
  it("shows an area's pages with the current one marked", () => {
    render(<SideNav ctx={ctx({ applications: 1 })} area="deployments" path="/deployments/applications/x" />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Overview", "Airfields", "Releases", "Applications", "Hostssoon", "Machine imagessoon"]);
    expect(screen.getByRole("link", { name: "Applications" })).toHaveAttribute("aria-current", "page");
  });
  it("shows the pages that are not built yet as soon, not hidden", () => {
    render(<SideNav ctx={ctx()} area="traffic" path="/traffic/protection" />);
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual(["Traffic mapsoon", "Protectionsoon", "Reliabilitysoon", "Switchessoon"]);
  });
  it("counts what waits for a decision on Activity", () => {
    render(<SideNav ctx={ctx({ approvals: 2 })} area="activity" path="/activity" />);
    expect(screen.getByLabelText("2 waiting")).toBeInTheDocument();
  });
  it("is not drawn for an area with a single page", () => {
    const { container } = render(<SideNav ctx={ctx()} area="projects" path="/projects" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("Breadcrumbs", () => {
  it("links back up and marks the page itself", () => {
    render(<Breadcrumbs path="/projects/shop/pipeline/edit" />);
    expect(screen.getByRole("navigation", { name: "You are here" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Pipelines" })).toHaveAttribute("href", "/pipelines");
    expect(screen.getByRole("link", { name: "shop" })).toHaveAttribute("href", "/projects/shop/pipeline");
    expect(screen.getByText("Edit")).toHaveAttribute("aria-current", "page");
  });
  it("is not drawn on an area's own page", () => {
    const { container } = render(<Breadcrumbs path="/projects" />);
    expect(container).toBeEmptyDOMElement();
  });
});
