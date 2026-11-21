import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Guide } from "../shell/Guide";
import { NO_FACTS } from "../nav/model";
import { me } from "../test/fixtures";
import type { Article, Manifest, SearchIndex } from "./content";

const manifest: Manifest = {
  pages: [
    { slug: "start-here", title: "Start here", section: "start", desc: "" },
    { slug: "concepts/approvals", title: "Approvals", section: "concepts", desc: "" },
  ],
  sections: [
    { id: "start", title: "Start here", blurb: "What it is.", items: [{ slug: "start-here", title: "Start here" }] },
    { id: "concepts", title: "Concepts", blurb: "The ideas.", items: [{ slug: "concepts/approvals", title: "Approvals" }] },
  ],
  paths: [{ id: "new", title: "I am new to Launchway", who: "You have not used it.", steps: ["start-here", "concepts/approvals"] }],
  palette: [["#111111", "#eeeeee"]],
};
const article: Article = {
  slug: "start-here", title: "Start here", section: "start",
  html: '<p>Hello <a href="/activity/approvals" class="lx-app">approvals</a>.</p><h2 id="two">Two</h2><div class="lx-try language-yaml"><pre><code><span class="c0">pipeline: x</span>\n</code></pre></div>',
  toc: [{ level: 2, id: "two", text: "Two" }],
  prev: null, next: { slug: "concepts/approvals", title: "Approvals", section: "concepts" },
};
const index: SearchIndex = { pages: ["start-here"], chunks: [[0, "two", "Two", "A snippet about approvals"]], terms: { approvals: [0, 4], approval: [0, 4] } };

vi.mock("./content", async (orig) => {
  const real = await orig<typeof import("./content")>();
  return { ...real, loadManifest: async () => manifest, loadArticle: async () => article, loadIndex: async () => index };
});
vi.mock("../api", async (orig) => {
  const real = await orig<typeof import("../api")>();
  return { ...real, api: { ...real.api, info: async () => ({ version: "v1.2.3", auth_mode: "none" }) } };
});

import { LearnRoot, learnRoute } from "./Learn";

beforeEach(() => { history.replaceState(null, "", "/"); localStorage.clear(); });

describe("learnRoute", () => {
  it("tells the home, the search and a page apart", () => {
    expect(learnRoute("/learn")).toEqual({ kind: "home" });
    expect(learnRoute("/learn/")).toEqual({ kind: "home" });
    expect(learnRoute("/learn/search")).toEqual({ kind: "search" });
    expect(learnRoute("/learn/concepts/approvals")).toEqual({ kind: "page", slug: "concepts/approvals" });
  });
});

describe("the Learn home", () => {
  it("offers a path for each kind of person, and says which release the pages describe", async () => {
    render(<LearnRoot path="/learn" />);
    expect(await screen.findByRole("heading", { name: "I am new to Launchway" })).toBeInTheDocument();
    expect(await screen.findByText(/These pages describe Launchway v1\.2\.3, the release you are running\./)).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Start here" })[0]).toHaveAttribute("href", "/learn/start-here");
  });
});

describe("a Learn page", () => {
  it("shows the text, the headings of the page and the next page", async () => {
    render(<LearnRoot path="/learn/start-here" />);
    expect(await screen.findByRole("heading", { level: 1, name: "Start here" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "approvals" })).toHaveAttribute("href", "/activity/approvals");
    expect(screen.getByRole("complementary", { name: "On this page" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Previous and next page" })).toHaveTextContent("Approvals");
  });
  it("gives a marked pipeline a button that opens it in the playground", async () => {
    const user = userEvent.setup();
    render(<LearnRoot path="/learn/start-here" />);
    await user.click(await screen.findByRole("button", { name: "Try it in the editor" }));
    expect(localStorage.getItem("launchway.playground")).toBe("pipeline: x\n");
    expect(location.pathname).toBe("/playground");
  });
});

describe("Learn search", () => {
  it("finds a section offline from the prebuilt index", async () => {
    history.replaceState(null, "", "/learn/search?q=approvals");
    render(<LearnRoot path="/learn/search" />);
    await waitFor(() => expect(screen.getByText(/A snippet about approvals/)).toBeInTheDocument());
    expect(document.querySelector(".learn-hits a")).toHaveAttribute("href", "/learn/start-here#two");
  });
});

describe("the Guide", () => {
  it("links to the page that explains the one you are on", () => {
    render(<Guide ctx={{ me: me(), facts: NO_FACTS }} path="/activity/approvals" explainer={null} onClose={() => {}} />);
    expect(screen.getByRole("link", { name: "Learn more about approvals" })).toHaveAttribute("href", "/learn/concepts/approvals");
  });
  it("does not point at Learn from inside Learn", () => {
    render(<Guide ctx={{ me: me(), facts: NO_FACTS }} path="/learn/concepts/approvals" explainer={null} onClose={() => {}} />);
    expect(screen.queryByRole("link", { name: /Open Learn/ })).toBeNull();
  });
});
