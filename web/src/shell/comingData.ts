// The pages whose features Launchway does not have yet. Each says what it will do, what will appear
// there and what makes it appear, in plain words, and offers the one action that exists today. They
// are real pages, not mock-ups: nothing on them is invented data.

import { terms } from "../terms";
import { prose } from "../prose";
const P = await prose("shell.comingData");

export interface Coming {
  title: string;
  /** The area's name, for the eyebrow. */
  area: string;
  /** One sentence: what this page will do. */
  does: string;
  /** What will appear here, and what makes it appear. */
  appears: string;
  /** What can be done today. */
  action?: { label: string; href: string };
}

export const COMING: Record<string, Coming> = {
  "/code/files": {
    title: "Files, commits and branches", area: "Code",
    does: P.t0,
    appears: P.t1,
    action: { label: "Add a {project}", href: "/projects/new" },
  },
  "/code/reviews": {
    title: "Reviews", area: "Code",
    does: P.t2,
    appears: P.t3,
    action: { label: "See runs", href: "/activity" },
  },
  "/depot/charts": {
    title: "Charts", area: "Images",
    does: P.t4,
    appears: P.t5,
    action: { label: "See images", href: "/depot" },
  },
  "/depot/files": {
    title: "Files", area: "Images",
    does: P.t6,
    appears: P.t7,
    action: { label: "See runs", href: "/activity" },
  },
  "/deployments/hosts": {
    title: "Hosts", area: "Deployments",
    does: P.t8,
    appears: P.t9,
    action: { label: "Open {runners}", href: "/settings/runners" },
  },
  "/deployments/machine-images": {
    title: "Machine images", area: "Deployments",
    does: P.t10,
    appears: P.t11,
    action: { label: "See images", href: "/depot" },
  },
  "/traffic": {
    title: "Traffic map", area: "Traffic",
    does: P.t12,
    appears: P.t13,
    action: { label: "Add a signal source", href: P.t14 },
  },
  "/traffic/protection": {
    title: "Protection", area: "Traffic",
    does: P.t15,
    appears: P.t16,
    action: { label: "Add a signal source", href: P.t14 },
  },
  "/traffic/reliability": {
    title: "Reliability", area: "Traffic",
    does: P.t17,
    appears: P.t18,
    action: { label: "Add a signal source", href: P.t14 },
  },
  "/traffic/toggles": {
    title: "{Toggles}", area: "Traffic",
    does: P.t19,
    appears: P.t20,
  },
  "/changes": {
    title: "Calendar", area: "Changes",
    does: P.t21,
    appears: P.t22,
    action: { label: "See runs", href: "/activity" },
  },
  "/changes/records": {
    title: "Change records", area: "Changes",
    does: P.t23,
    appears: P.t24,
    action: { label: "Open the {logbook}", href: "/settings/audit" },
  },
  "/changes/freezes": {
    title: "{Hold} windows", area: "Changes",
    does: P.t25,
    appears: P.t26,
  },
  "/changes/rules": {
    title: "{Clearance} rules", area: "Changes",
    does: P.t27,
    appears: P.t28,
    action: { label: "Open the rules", href: "/settings/rules" },
  },
  "/changes/tickets": {
    title: "Ticket system", area: "Changes",
    does: P.t29,
    appears: P.t30,
  },
};

/** The page's entry with the words in effect filled in. */
export function comingFor(path: string): Coming | undefined {
  const c = COMING[path];
  if (!c) return undefined;
  const t = terms();
  return { ...c, title: t.say(c.title), does: t.say(c.does), appears: t.say(c.appears), action: c.action && { ...c.action, label: t.say(c.action.label) } };
}

/** The addresses that have a coming page. */
export function isComing(path: string): boolean {
  return Object.prototype.hasOwnProperty.call(COMING, path);
}
