// Approvals by several people (schema 27): the fields a step or a waiting
// approval gets when it asks for more than one approval or names approvers.

export interface ApprovalRecord { actor: string; at: number; comment?: string }

export interface ApprovalProgress {
  approvals?: ApprovalRecord[];
  approvals_needed?: number;
  approvals_needed_from?: string;
  approvers?: { groups?: string[]; users?: string[]; count: number };
}

/** "1 of 2 approved (ann)", or "" when the approval asks for one. */
export function progressText(p: ApprovalProgress): string {
  const needed = p.approvals_needed ?? 1;
  const names = (p.approvals ?? []).map((a) => a.actor);
  if (needed < 2) return "";
  return `${names.length} of ${needed} approved${names.length ? ` (${names.join(", ")})` : ""}` +
    (p.approvals_needed_from ? `; the number comes from ${p.approvals_needed_from}` : "");
}

/** Who may decide, in words, or "" when anyone with the operator role may. */
export function whoMayDecide(p: ApprovalProgress): string {
  const a = p.approvers;
  if (!a || (!a.groups?.length && !a.users?.length)) return "";
  const parts = [];
  if (a.groups?.length) parts.push(`members of ${a.groups.join(" or ")}`);
  if (a.users?.length) parts.push(a.users.join(" or "));
  return parts.join(", or ");
}
