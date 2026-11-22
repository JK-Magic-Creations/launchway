// Calls for verified commits and tags. They mirror internal/api/signing.go.

import { call } from "./api";

export type SignatureState = "verified" | "unknown_key" | "bad_signature" | "unsigned" | "key_revoked" | "key_expired" | "unsupported";

export interface SignatureResult {
  object: string; kind: "commit" | "tag"; state: SignatureState; explanation: string; signature_type?: string; key_fingerprint?: string; key_name?: string;
  signer?: string; installation_key?: boolean; signer_is_author?: boolean; signer_is_committer?: boolean;
}

export interface SigningKey {
  id: string; owner: string; kind: string; name: string; type: string; fingerprint: string; public_key: string; created_at: string; expires_at?: string; revoked_at?: string;
}

const enc = encodeURIComponent;

export const signingApi = {
  keys: () => call<{ items: SigningKey[] }>("GET", "/api/v1/user/signing-keys").then((r) => r.items),
  addKey: (b: { name: string; public_key: string; expires_at?: string }) => call<SigningKey>("POST", "/api/v1/user/signing-keys", b),
  revokeKey: (id: string) => call<SigningKey>("POST", `/api/v1/user/signing-keys/${enc(id)}/revoke`),
  deleteKey: (id: string) => call<void>("DELETE", `/api/v1/user/signing-keys/${enc(id)}`),
  signatures: (repo: string, shas: string[]) =>
    call<{ items: SignatureResult[] }>("GET", `/api/v1/repositories/${repo.split("/").map(enc).join("/")}/signatures?shas=${enc(shas.join(","))}`).then((r) => r.items),
  installationKey: () => call<{ public_key: string; fingerprint: string; address: string }>("GET", "/api/v1/signing/installation-key"),
};
