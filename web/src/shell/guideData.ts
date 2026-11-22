// What the Guide says about each page, in plain words: where you are, and what you can do next. Plain
// text per page, kept as data so it is reviewed as writing, not as code. A page without an entry gets
// its area's sentence.

import type { IconName } from "../kit/Icon";
import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("shell.guideData");

export interface GuideEntry {
  title: string;
  text: string;
  /** Things to do next; each is a link. */
  does?: { icon: IconName; label: string; href: string }[];
}

export const GUIDE: Record<string, GuideEntry> = {
  "/": {
    title: "You are on Home",
    text: P.t0,
    does: [
      { icon: "plus", label: "Add a {project}", href: "/projects/new" },
      { icon: "gate", label: "See what is waiting for you", href: "/activity/approvals" },
      { icon: "activity", label: "See every {run}", href: "/activity" },
    ],
  },
  "/projects": {
    title: "Your {projects}",
    text: P.t1,
    does: [{ icon: "plus", label: "Add a {project}", href: "/projects/new" }],
  },
  "/repositories/:owner/:name/pulls": {
    title: "Pull requests",
    text: P.t2,
  },
  "/repositories/:owner/:name/pulls/new": {
    title: "A new pull request",
    text: P.t3,
  },
  "/repositories/:owner/:name/pulls/:number": {
    title: "A pull request",
    text: P.t4,
    does: [{ icon: "activity", label: "See every {run}", href: "/activity" }],
  },
  "/projects/:name": {
    title: "A project",
    text: P.t5,
    does: [{ icon: "activity", label: "See every {run}", href: "/activity" }],
  },
  "/pipelines": {
    title: "Pipelines",
    text: P.t6,
    does: [{ icon: "play", label: "Try a pipeline without saving", href: "/playground" }],
  },
  "/depot": {
    title: "Images",
    text: P.t7,
    does: [{ icon: "search", label: P.t8, href: "/depot/components" }],
  },
  "/depot/components": {
    title: "Components",
    text: P.t9,
    does: [{ icon: "package", label: "Find an image", href: "/depot" }],
  },
  "/deployments": {
    get title() { return terms().say("{Deploys}"); },
    text: P.t10,
    does: [{ icon: "plus", label: "Add an {environment}", href: P.t11 }],
  },
  "/deployments/environments": {
    title: "{Environments}",
    text: P.t12,
    does: [{ icon: "plus", label: "Add an {environment}", href: P.t11 }],
  },
  "/activity": {
    title: "Activity",
    text: P.t13,
    does: [{ icon: "gate", label: "Decide what is waiting", href: "/activity/approvals" }],
  },
  "/activity/approvals": {
    title: "Waiting for a decision",
    text: P.t14,
    does: [{ icon: "users", label: "See who may give {clearance}", href: "/settings/access" }],
  },
  "/runs/:id": {
    title: "A run",
    text: P.t15,
    does: [{ icon: "activity", label: "All {runs}", href: "/activity" }],
  },
  "/library": {
    title: "The Library",
    text: P.t16,
  },
  "/settings": {
    title: "Settings",
    text: P.t17,
    does: [{ icon: "users", label: "People and access", href: "/settings/access" }],
  },
  "/settings/runners": {
    title: "{Runners}",
    text: P.t18,
    does: [{ icon: "plus", label: "Add a {runner}", href: "/config/wizard/add-runner" }],
  },
  "/settings/variables": {
    title: "Variables",
    text: P.t19,
  },
  "/projects/:name/variables": {
    title: "Variables of this {project}",
    text: P.t20,
  },
  "/settings/words": {
    title: "Words",
    text: P.t21,
  },
  "/settings/audit": {
    title: "{Logbook}",
    text: P.t22,
  },
  "/learn": {
    title: "{Learn}",
    text: P.t23,
  },
  "/learn/:a": {
    title: "{Learn}",
    text: P.t24,
  },
  "/learn/:a/:b": {
    title: "{Learn}",
    text: P.t24,
  },
  "/kit": {
    title: "The design kit",
    text: P.t25,
  },
};

/** The entry for a page; the area's own sentence when the page has none. The words in effect are filled in. */
export function guideFor(pattern: string, fallbackTitle: string, fallbackText: string): GuideEntry {
  const t = terms();
  const e = GUIDE[pattern] ?? { title: fallbackTitle, text: fallbackText };
  return { ...e, title: t.say(e.title), text: t.say(e.text), does: e.does?.map((d) => ({ ...d, label: t.say(d.label) })) };
}
