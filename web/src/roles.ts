import { terms } from "./terms";

/** The word a person reads for a role. The role itself (viewer, operator, admin) is what the API and the files say and
 * never changes; this is its display name in the installation's words: Observer, Pilot and Commander in the aviation set. */
export function roleName(role: string | undefined): string {
  switch (role) {
    case "viewer": return terms().say("{Viewer}");
    case "operator": return terms().say("{Developer}");
    case "admin": return terms().say("{Owner}");
    case "approver": return terms().say("{Approver}");
    default: return role ?? "";
  }
}
