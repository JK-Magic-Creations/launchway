import { createContext, useContext } from "react";
import type { Me, Role } from "./api";

// Who is signed in. The server decides every request; this only lets pages
// hide buttons that would be refused.
export const MeContext = createContext<Me | null>(null);

export function useMe(): Me | null {
  return useContext(MeContext);
}

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 };

/** Whether the person may start, cancel, resume, approve and reject runs of a project. */
export function canOperate(me: Me | null, project: string): boolean {
  const role = me?.roles[project];
  return !!role && RANK[role] >= RANK.operator;
}

/** Whether the person may change a repository's code (branches, tags, files): an administrator of everything, or an
 * operator of it. Roles name projects, so a hosted repository has none of its own listed for most people; the server
 * decides in the end and says why when it refuses. */
export function canChange(me: Me | null, repo: string): boolean {
  return !!me && (me.admin || canOperate(me, repo));
}
