/** Where to send someone after they sign in, from where they are now. */
export function nextFromHere(): string {
  if (location.pathname === "/signin") {
    const n = new URLSearchParams(location.search).get("next");
    return n && n.startsWith("/") && !n.startsWith("//") ? n : "/";
  }
  return location.pathname + location.search;
}
