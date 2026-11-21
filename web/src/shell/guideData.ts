// What the Guide says about each page, in plain words: where you are, and what you can do next. Plain
// text per page, kept as data so it is reviewed as writing, not as code. A page without an entry gets
// its area's sentence.

import type { IconName } from "../kit/Icon";
import { terms } from "../terms";

export interface GuideEntry {
  title: string;
  text: string;
  /** Things to do next; each is a link. */
  does?: { icon: IconName; label: string; href: string }[];
}

export const GUIDE: Record<string, GuideEntry> = {
  "/": {
    title: "You are on Home",
    text: "Every change you make travels along a line, from the commit to running software. Home shows what needs you, then the changes on their way and where each one is.",
    does: [
      { icon: "plus", label: "Add a project", href: "/config/wizard/connect-repository" },
      { icon: "gate", label: "See what is waiting for you", href: "/activity/approvals" },
      { icon: "activity", label: "See every run", href: "/activity" },
    ],
  },
  "/projects": {
    title: "Your projects",
    text: "A project is one piece of software, kept in a Git repository. Open one to see its pipeline, its runs and where it is running.",
    does: [{ icon: "plus", label: "Add a project", href: "/config/wizard/connect-repository" }],
  },
  "/projects/:name": {
    title: "A project",
    text: "Everything about one piece of software: what it is, where it comes from, who uses it, what happened to it and what you can do with it.",
    does: [{ icon: "activity", label: "See every run", href: "/activity" }],
  },
  "/pipelines": {
    title: "Pipelines",
    text: "A pipeline is a set of steps that runs when something happens, such as a push. Each row says which project it belongs to and how its last run went.",
    does: [{ icon: "play", label: "Try a pipeline without saving", href: "/playground" }],
  },
  "/images": {
    title: "Images",
    text: "Everything your pipelines build lives here. Find an image by its project, its repository or its digest, then see which run built it.",
    does: [{ icon: "search", label: "Find a component in every run", href: "/images/components" }],
  },
  "/images/components": {
    title: "Components",
    text: "What is inside your software, found by name across every run. Each hit says which run and which project, so you know what a new problem touches.",
    does: [{ icon: "package", label: "Find an image", href: "/images" }],
  },
  "/deployments": {
    title: "Deployments",
    text: "Where your software is running. Each environment shows what is deployed there and whether it is healthy. Anything that is not healthy tells you why.",
    does: [{ icon: "plus", label: "Add an environment", href: "/config/wizard/add-environment" }],
  },
  "/deployments/environments": {
    title: "Environments",
    text: "An environment is a named place your software goes, such as staging or production. Open one to see what runs there and who put it there.",
    does: [{ icon: "plus", label: "Add an environment", href: "/config/wizard/add-environment" }],
  },
  "/activity": {
    title: "Activity",
    text: "Every run, newest first, and everything waiting for a person. Filter by status to find the runs that stopped.",
    does: [{ icon: "gate", label: "Decide what is waiting", href: "/activity/approvals" }],
  },
  "/activity/approvals": {
    title: "Waiting for a decision",
    text: "A run stops here when a rule needs {clearance} from a person. Read what it will do, then say {Go} or {No_go}. Your choice is recorded with your name.",
    does: [{ icon: "users", label: "See who may give {clearance}", href: "/settings/access" }],
  },
  "/runs/:id": {
    title: "A run",
    text: "A run puts one version of your code through the pipeline. Watch the line fill in from left to right, and choose a step to read its log. If a step stopped, the red box says why in plain words.",
    does: [{ icon: "activity", label: "All runs", href: "/activity" }],
  },
  "/library": {
    title: "The Library",
    text: "Ready-made pieces for pipelines. Pick a template to start from something that already works.",
  },
  "/settings": {
    title: "Settings",
    text: "Set up once, then rarely come back. Start with People and access, then add a runner so steps have somewhere to run.",
    does: [{ icon: "users", label: "People and access", href: "/settings/access" }],
  },
  "/settings/runners": {
    title: "Runners",
    text: "Runners are the machines that do the work of your steps. Each says when it was last seen, so you know which are answering.",
    does: [{ icon: "plus", label: "Add a runner", href: "/config/wizard/add-runner" }],
  },
  "/settings/words": {
    title: "Words",
    text: "Launchway's own words for its levels and parts: the {guide} and the {logbook}. Choose a set or rename any word; only the labels change.",
  },
  "/settings/audit": {
    title: "{Logbook}",
    text: "Every sign-in and every change, with who did it and when. Search for a person, an action or an object.",
  },
  "/learn": {
    title: "Learn",
    text: "The documentation of the release you are running, here in the product. Choose a path, or search. Nothing leaves this server.",
  },
  "/learn/:a": {
    title: "Learn",
    text: "You are reading the documentation of this release. The contents are on the left, and the headings of the page on the right.",
  },
  "/learn/:a/:b": {
    title: "Learn",
    text: "You are reading the documentation of this release. The contents are on the left, and the headings of the page on the right.",
  },
  "/kit": {
    title: "The design kit",
    text: "The parts every Launchway screen is built from, in light and dark. Change the theme with the moon button and look at each part in both.",
  },
};

/** The entry for a page; the area's own sentence when the page has none. The words in effect are filled in. */
export function guideFor(pattern: string, fallbackTitle: string, fallbackText: string): GuideEntry {
  const t = terms();
  const e = GUIDE[pattern] ?? { title: fallbackTitle, text: fallbackText };
  return { ...e, title: t.say(e.title), text: t.say(e.text), does: e.does?.map((d) => ({ ...d, label: t.say(d.label) })) };
}
