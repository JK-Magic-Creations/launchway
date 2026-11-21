// Deep links into Argo CD, the deploy engine Launchway connects to. Launchway explains; the engine's own
// console shows the rest. The address of the console is kept in this browser, because the server is not told it.

const KEY = "lw-argocd-url";

export function storedArgoUrl(): string {
  try { return localStorage.getItem(KEY) ?? ""; } catch { return ""; }
}

export function saveArgoUrl(url: string): string {
  const clean = normalizeArgoUrl(url);
  try { if (clean) localStorage.setItem(KEY, clean); else localStorage.removeItem(KEY); } catch { /* storage may be blocked */ }
  return clean;
}

/** The console's address without a trailing slash, or "" when it is not a web address. */
export function normalizeArgoUrl(url: string): string {
  const u = url.trim().replace(/\/+$/, "");
  return /^https?:\/\/[^\s/]+/.test(u) ? u : "";
}

/** The console's page for an application, and for one of its resources when known. */
export function argoLink(base: string, application: string, resource?: { kind: string; name: string; namespace?: string; group?: string }): string {
  const app = `${base}/applications/${encodeURIComponent(application)}`;
  if (!resource) return app;
  const node = [resource.group ?? "", resource.kind, resource.namespace ?? "", resource.name, "0"].map(encodeURIComponent).join("%2F");
  return `${app}?view=tree&node=${node}&resource=`;
}
