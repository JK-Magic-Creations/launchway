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
