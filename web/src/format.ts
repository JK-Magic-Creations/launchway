export function duration(ms: number): string {
  if (ms < 0) ms = 0;
  if (ms < 1000) return `${Math.round(ms / 100) / 10}s`;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

export function ago(at: number, now = Date.now()): string {
  if (!at) return "";
  const s = Math.max(0, Math.floor((now - at) / 1000));
  if (s < 10) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}d ago`;
  return new Date(at).toLocaleDateString();
}

export function clock(at: number): string {
  if (!at) return "";
  return new Date(at).toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit",
  });
}

export function shortSha(sha: string): string {
  return sha ? sha.slice(0, 8) : "";
}

// runTime is how long a run or step has taken so far, or took.
export function elapsed(start: number, end: number, now: number): string {
  if (!start) return "";
  return duration((end || now) - start);
}

// shortDuration writes a Go duration without trailing zero parts, as the
// server's Duration.Short does: "1h0m0s" is "1h", "1h30m0s" is "1h30m".
export function shortDuration(d: string): string {
  return d.replace(/m0s$/, "m").replace(/h0m$/, "h");
}

/** Where a run came from, as shown beside it: "pull request #12", a branch or a tag. */
export function refLabel(r: { branch?: string; tag?: string; pull_request?: number }): string {
  if (r.pull_request) return `pull request #${r.pull_request}`;
  return r.branch || r.tag || "";
}

/** Who started a run, in words. */
export function starterLabel(r: { event: string; author: string; actor: string; draft: boolean }): string {
  if (r.event === "push") return `pushed by ${r.author || r.actor}`;
  if (r.event === "pull_request") return `pull request by ${r.author || r.actor}`;
  if (r.draft) return `draft run by ${r.actor || "someone"}`;
  return `started by ${r.actor || "someone"}`;
}
