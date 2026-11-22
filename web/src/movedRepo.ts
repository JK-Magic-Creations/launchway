import { replace } from "./router";

/** A repository opened at an address it used to have answers from where it is now (the server redirects the API call):
 * the page's own address follows it, so a bookmark or a link from before the rename ends up at the new one. */
export function followMove(asked: string, actual: string) {
  if (!actual || asked === actual) return;
  const from = "/repositories/" + asked.split("/").map(encodeURIComponent).join("/");
  const to = "/repositories/" + actual.split("/").map(encodeURIComponent).join("/");
  if (location.pathname === from || location.pathname.startsWith(from + "/")) replace(to + location.pathname.slice(from.length) + location.search);
}
