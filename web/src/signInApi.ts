import { call } from "./api";

/** Present on /me only while this installation runs with no sign-in on an address others can reach. */
export interface SignInOff { warning: string; next: string; requested_at?: number }

export const signInApi = {
  /** Asks for built-in sign-in: a one-time setup code (shown once), and the servers use it from their next restart. */
  setupNow: () => call<{ setup_code: string; next: string }>("POST", "/api/v1/auth/setup-now", {}),
};
