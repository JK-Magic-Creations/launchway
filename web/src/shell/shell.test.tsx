import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { me, project, run } from "../test/fixtures";
import { NO_FACTS, SECTIONS, locate, redirectFor } from "../nav/model";
import { COMING, comingTitle, isComing } from "./comingData";
import { ComingPage } from "./Coming";
import { DoBar } from "./DoBar";
import { doItems, searchItems } from "./doItems";
import { opensDoBar, pulseOf } from "./Frame";
import { Launcher } from "./Launcher";
import { GUIDE } from "./guideData";

const ctx = (m = me()) => ({ me: m, facts: NO_FACTS });

describe("the do bar's search", () => {
  const items = doItems(ctx(), [project({ name: "shop-api", last_run: run({ status: "failed", number: 7, id: "run_7" }) }), project({ name: "shop-web" })]);
  it("offers the actions and wizards that exist, and nothing for a person who may not configure", () => {
    expect(searchItems(items, "").map((i) => i.label)).toContain("Add a project");
    const viewer = doItems(ctx(me({ admin: false })), []);
    expect(viewer.map((i) => i.label)).not.toContain("Add a project");
    expect(viewer.map((i) => i.label)).toContain("Decide what is waiting");
  });
  it("finds a project by name and the failed run of one", () => {
    expect(searchItems(items, "shop").map((i) => i.href)).toContain("/projects/shop-api");
    const why = searchItems(items, "why").find((i) => i.id === "why-shop-api");
    expect(why?.href).toBe("/runs/run_7");
    expect(items.some((i) => i.id === "why-shop-web")).toBe(false);
  });
  it("finds every page of every area, the soon pages marked", () => {
    const hits = searchItems(items, "hold");
    expect(hits[0]).toMatchObject({ href: "/changes/freezes" });
    expect(hits[0].label).toContain("(soon)");
  });
  it("matches every word typed", () => {
    expect(searchItems(items, "add runner").map((i) => i.href)).toContain("/config/wizard/add-runner");
    expect(searchItems(items, "zzz nothing")).toEqual([]);
  });
});

describe("DoBar", () => {
  it("is a dialog that takes focus, moves with the arrow keys, and closes with Escape", async () => {
    const user = userEvent.setup();
    const close = vi.fn();
    render(<DoBar ctx={ctx()} projects={[]} onClose={close} />);
    expect(screen.getByRole("dialog", { name: "What do you want to do?" })).toBeInTheDocument();
    expect(screen.getByRole("combobox")).toHaveFocus();
    await user.type(screen.getByRole("combobox"), "approv");
    expect(screen.getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Escape}");
    expect(close).toHaveBeenCalled();
  });
  it("says what it did not find, in plain words", async () => {
    const user = userEvent.setup();
    render(<DoBar ctx={ctx()} projects={[]} onClose={() => {}} />);
    await user.type(screen.getByRole("combobox"), "qqqq");
    expect(screen.getByText(/Nothing matches "qqqq"/)).toBeInTheDocument();
  });
});

describe("the slash key", () => {
  const key = (k: string, target: Partial<HTMLElement> = {}, mods = {}) => ({ key: k, ctrlKey: false, metaKey: false, altKey: false, target: target as EventTarget, ...mods });
  it("opens the bar outside a field only", () => {
    expect(opensDoBar(key("/", { tagName: "BODY" }))).toBe(true);
    expect(opensDoBar(key("/", { tagName: "INPUT" }))).toBe(false);
    expect(opensDoBar(key("/", { tagName: "TEXTAREA" }))).toBe(false);
    expect(opensDoBar(key("/", { tagName: "DIV" }, { ctrlKey: true }))).toBe(false);
    expect(opensDoBar(key("a", { tagName: "BODY" }))).toBe(false);
    // A person can turn the single-key shortcut off.
    localStorage.setItem("lw-shortcut", "off");
    expect(opensDoBar(key("/", { tagName: "BODY" }))).toBe(false);
    localStorage.removeItem("lw-shortcut");
  });
});

describe("the pulse", () => {
  it("is empty when all is well, and loud only when a person is needed", () => {
    expect(pulseOf(0, 0, false)).toBe("quiet");
    expect(pulseOf(0, 0, true)).toBe("running");
    expect(pulseOf(0, 2, true)).toBe("waiting");
    expect(pulseOf(1, 2, true)).toBe("failing");
  });
});

describe("pages that are not built yet", () => {
  it("every page marked soon has an honest page that says what it will do and what makes it appear", () => {
    for (const s of SECTIONS) for (const t of s.tabs.filter((x) => x.soon)) {
      expect(isComing(t.href), t.href).toBe(true);
      expect(locate(t.href), t.href).not.toBeNull();
    }
    for (const [path, c] of Object.entries(COMING)) {
      expect(locate(path), path).not.toBeNull();
      expect(c.does.length, path).toBeGreaterThan(40);
      expect(c.appears.length, path).toBeGreaterThan(40);
    }
  });
  it("sends no one to a file to edit (R-I7) and names no other product (R-A4)", () => {
    for (const c of Object.values(COMING)) {
      expect(c.does + c.appears).not.toMatch(/\.ya?ml|edit the file|config file/i);
    }
  });
  it("renders what it will do, with no sample numbers, and its one action", () => {
    render(<ComingPage path="/traffic" />);
    expect(screen.getByRole("heading", { level: 1, name: /Traffic map/ })).toBeInTheDocument();
    expect(screen.getByText("Coming")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Add a signal source/ })).toHaveAttribute("href", "/config/wizard/add-health-source");
    expect(screen.getByText(/shows no sample numbers/)).toBeInTheDocument();
  });
  it("says there is nothing at an address that has none", () => {
    render(<ComingPage path="/traffic/nope" />);
    expect(screen.getByText("Nothing here")).toBeInTheDocument();
    expect(comingTitle("/traffic/nope")).toBe("nope");
  });
});

describe("the launcher", () => {
  it("lists every area with its sentence and its pages", () => {
    render(<Launcher ctx={ctx()} onClose={() => {}} />);
    expect(screen.getByRole("dialog", { name: "All areas" })).toBeInTheDocument();
    for (const s of SECTIONS) expect(screen.getByText(s.promise)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Hold windows (soon)" })).toHaveAttribute("href", "/changes/freezes");
  });
});

describe("old addresses", () => {
  it("keep working: the registry moved to Images", () => {
    expect(redirectFor("/library/images")).toBe("/images");
    expect(redirectFor("/library/components", "?name=x")).toBe("/images/components?name=x");
  });
});

describe("the Guide's text", () => {
  it("is plain words: no entry tells a person to edit a file", () => {
    for (const g of Object.values(GUIDE)) expect(g.text).not.toMatch(/\.ya?ml|edit the file/i);
  });
  it("only has entries for pages that exist", () => {
    const patterns = new Set(["/learn", "/learn/:a", "/learn/:a/:b", "/", "/projects", "/projects/:name", "/pipelines", "/images", "/images/components", "/deployments", "/deployments/environments", "/activity", "/activity/approvals", "/runs/:id", "/library", "/settings", "/settings/runners", "/settings/audit", "/settings/words", "/settings/variables", "/projects/:name/variables", "/kit", "/repositories/:owner/:name/pulls", "/repositories/:owner/:name/pulls/new", "/repositories/:owner/:name/pulls/:number"]);
    for (const k of Object.keys(GUIDE)) expect(patterns.has(k), k).toBe(true);
  });
});
