/**
 * Whether an address is a path on this server and nothing else: it starts with one slash, has no backslash (a browser
 * reads /\host as //host), no whitespace or control characters, and no scheme or host. Anything else is "/", so the
 * sign-in page can never send a person to another site. The server applies the same rule (safeNext in login.go).
 */
export function safeNext(next: string | null | undefined): string {
  if (!next || next.length > 2000 || next[0] !== "/" || next.startsWith("//") || next.includes("\\")) return "/";
  // eslint-disable-next-line no-control-regex
  if (/[\u0000- \u007f-\u009f]/.test(next)) return "/";
  try {
    const u = new URL(next, "http://same.invalid");
    if (u.origin !== "http://same.invalid") return "/";
  } catch { return "/"; }
  return next;
}

/** Where to send someone after they sign in, from where they are now. */
export function nextFromHere(): string {
  if (location.pathname === "/signin") return safeNext(new URLSearchParams(location.search).get("next"));
  return safeNext(location.pathname + location.search);
}
