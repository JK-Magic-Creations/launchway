// Where a repository lives on the web, when its address says so. A local path or a file address has no web page.

/** The repository's page on its Git host, or null when the address is not a web one. */
export function hostBase(url: string): string | null {
  const u = url.trim();
  let base = "";
  const ssh = /^(?:ssh:\/\/)?git@([^:/]+)[:/](.+)$/.exec(u);
  if (/^https?:\/\//.test(u)) base = u;
  else if (ssh) base = `https://${ssh[1]}/${ssh[2]}`;
  else return null;
  return base.replace(/\.git$/, "").replace(/\/+$/, "").replace(/^(https?:\/\/)[^@/]+@/, "$1");
}

/** The host's own name for the address, such as git.example.com, for a link's text. */
export function hostName(url: string): string | null {
  const b = hostBase(url);
  if (!b) return null;
  try { return new URL(b).host; } catch { return null; }
}

/** The page of a branch on the host. Most hosts use /tree/<branch>. */
export function branchLink(url: string, branch: string): string | null {
  const b = hostBase(url);
  return b ? `${b}/tree/${encodeURIComponent(branch).replace(/%2F/g, "/")}` : null;
}

