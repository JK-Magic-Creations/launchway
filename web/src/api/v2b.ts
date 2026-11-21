// Small read-only calls added for the v2 pages (internal/api/approvalsettings.go).
import { call } from "../api";

/** How this installation treats the person who started a run. */
export interface ApprovalSettings {
  /** The person who started a run may approve it. */
  allow_self: boolean;
  /** People sign in; without it everyone is the same user. */
  sign_in: boolean;
}

export const v2bApi = {
  approvalSettings: () => call<ApprovalSettings>("GET", "/api/v1/settings/approvals"),
};
