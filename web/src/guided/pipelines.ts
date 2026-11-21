import type { Graph } from "../api";

/** What starts a pipeline, in one plain sentence, from the parsed pipeline's triggers. */
export function startsSentence(g: Pick<Graph, "on"> | undefined): string {
  if (!g) return "What starts it is not known yet.";
  const on = g.on;
  const parts: string[] = [];
  const push = on.push ? [...(on.push.branches ?? []), ...(on.push.tags ?? []).map((t) => `tag ${t}`)] : [];
  if (on.push) parts.push(push.length ? `pushes to ${push.join(", ")}` : "every push");
  if (on.pull_request) parts.push(on.pull_request.into?.length ? `pull requests into ${on.pull_request.into.join(", ")}` : "pull requests");
  return parts.length ? `Starts on ${parts.join(" and ")}, and by hand.` : "Starts only by hand.";
}
