import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { entityHref } from "../entities";
import { plainEntityNames } from "../test/entities";
import { EntityLink, linkify, Linked } from "./EntityLink";

describe("entityHref", () => {
  it("gives every kind its own page", () => {
    expect(entityHref({ kind: "project", name: "shop api" })).toBe("/projects/shop%20api");
    expect(entityHref({ kind: "run", name: "#4", id: "run_x" })).toBe("/runs/run_x");
    expect(entityHref({ kind: "environment", name: "staging" })).toBe("/deployments/environments/staging");
    expect(entityHref({ kind: "application", name: "a" })).toBe("/deployments/applications/a");
    expect(entityHref({ kind: "template", name: "go", library: "platform" })).toBe("/library/templates/platform/go");
    expect(entityHref({ kind: "image", name: "sha256:1" })).toBe("/images?digest=sha256%3A1");
    expect(entityHref({ kind: "component", name: "lodash" })).toBe("/images/components?name=lodash");
    expect(entityHref({ kind: "pipeline", name: "p" })).toBe("/projects/p/pipeline");
  });
});

describe("EntityLink", () => {
  it("is a link with the name, that says what it opens", () => {
    render(<EntityLink kind="project" name="shop-api" />);
    const a = screen.getByRole("link", { name: "shop-api" });
    expect(a).toHaveAttribute("href", "/projects/shop-api");
    expect(a).toHaveAttribute("title", "Open the project shop-api");
  });
  it("shows other text when given, and still goes to the thing", () => {
    render(<EntityLink kind="run" name="" id="r1">See the run</EntityLink>);
    expect(screen.getByRole("link", { name: "See the run" })).toHaveAttribute("href", "/runs/r1");
  });
});

describe("linkify", () => {
  it("links whole names, longest first", () => {
    const parts = linkify("shop-api-dev differs; shop-api is fine", [{ kind: "project", name: "shop-api" }, { kind: "application", name: "shop-api-dev" }]);
    expect(parts).toEqual([{ kind: "application", name: "shop-api-dev" }, " differs; ", { kind: "project", name: "shop-api" }, " is fine"]);
  });
  it("does not link part of a longer word", () => {
    expect(linkify("shop-apis", [{ kind: "project", name: "shop-api" }])).toEqual(["shop-apis"]);
  });
  it("leaves text without names alone", () => { expect(linkify("nothing", [])).toEqual(["nothing"]); });
  it("renders links inside a sentence", () => {
    const { container } = render(<p><Linked text="Run #4 of shop-api failed" entities={[{ kind: "project", name: "shop-api" }]} /></p>);
    expect(container.textContent).toBe("Run #4 of shop-api failed");
    expect(container.querySelectorAll("a")).toHaveLength(1);
  });
});

describe("the lint", () => {
  it("finds a name written as plain text and accepts a linked one", () => {
    const { container, rerender } = render(<p>Deployed by shop-api</p>);
    expect(plainEntityNames(container, ["shop-api"])).toHaveLength(1);
    rerender(<p>Deployed by <EntityLink kind="project" name="shop-api" /></p>);
    expect(plainEntityNames(container, ["shop-api"])).toEqual([]);
  });
  it("lets a page's own title and a form control hold the name", () => {
    const { container } = render(<div><h1>shop-api</h1><select><option>shop-api</option></select></div>);
    expect(plainEntityNames(container, ["shop-api"])).toEqual([]);
  });
});
