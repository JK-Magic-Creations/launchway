// The titles of the pages whose features Launchway does not have yet, apart from their texts (comingData.ts),
// because the page titles are needed at the first load and the texts only when such a page is opened. A test
// keeps the two equal.
import { terms } from "../terms";

export const COMING_TITLES: Record<string, string> = {
  "/code/files": "Files, commits and branches", "/code/reviews": "Reviews", "/images/charts": "Charts", "/images/files": "Files",
  "/deployments/hosts": "Hosts", "/deployments/machine-images": "Machine images", "/traffic": "Traffic map", "/traffic/protection": "Protection",
  "/traffic/reliability": "Reliability", "/traffic/toggles": "{Toggles}", "/changes": "Calendar", "/changes/records": "Change records",
  "/changes/freezes": "{Hold} windows", "/changes/rules": "{Clearance} rules", "/changes/tickets": "Ticket system",
};

/** The title of a coming page, or the last word of its address when there is none. */
export function comingTitle(path: string): string {
  const t = COMING_TITLES[path];
  return t ? terms().say(t) : path.split("/").pop()?.replace(/-/g, " ") ?? "Not here";
}
