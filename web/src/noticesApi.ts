// Calls for the inbox and the choices of what a person is told. They mirror internal/api/notices.go.

import { call } from "./api";

export interface InboxItem {
  id: string; source: "notice" | "work" | "pull"; kind: string; title: string; detail?: string; repository?: string; link?: string; actor?: string;
  at: string; read: boolean; done: boolean; waiting_on_you?: boolean;
}
export interface Inbox { items: InboxItem[]; unread: number; waiting: number }

export interface KindChoice { kind: string; title: string; default: string; channel: "in_app" | "email" | "off"; waiting_on_you: boolean }
export interface Watch { scope: string; level: "all" | "participating" | "none" }
export interface NoticeSettings { quiet_start: number; quiet_end: number; zone: string; digest: boolean; digest_hour: number }
export interface Preferences { kinds: KindChoice[]; watches: Watch[]; settings: NoticeSettings; mail_on: boolean; address_known: boolean; channels: string[]; levels: string[] }

export interface MailStatus {
  enabled: boolean; host?: string; port?: number; security?: string; from?: string; public_url?: string; credentials: boolean; include_private_details: boolean;
  pending: number; sent: number; failed: number; sent_last_hour: number; rate_per_minute: number; rate_per_hour: number;
}

export const noticesApi = {
  inbox: (state: string, source: string, kind: string) => {
    const q = new URLSearchParams();
    if (state) q.set("state", state);
    if (source) q.set("source", source);
    if (kind) q.set("kind", kind);
    return call<Inbox>("GET", `/api/v1/inbox?${q}`);
  },
  count: () => call<{ unread: number; waiting: number }>("GET", "/api/v1/inbox/count"),
  read: (ids: string[], value = true) => call<{ changed: number }>("PUT", "/api/v1/inbox/read", { ids, value }),
  done: (ids: string[], value = true) => call<{ changed: number }>("PUT", "/api/v1/inbox/done", { ids, value }),
  preferences: () => call<Preferences>("GET", "/api/v1/notices/preferences"),
  save: (b: { kinds?: Record<string, string>; watches?: Watch[]; settings?: NoticeSettings }) => call<Preferences>("PUT", "/api/v1/notices/preferences", b),
  mail: () => call<MailStatus>("GET", "/api/v1/mail"),
  testMail: (to?: string) => call<{ sent: boolean; to?: string; error?: string }>("POST", "/api/v1/mail/test", { to: to ?? "" }),
};
