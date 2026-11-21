// The call for encryption at rest. It mirrors internal/api/encryption.go (GET /api/v1/encryption):
// the report holds no key material, only which holder keeps the root key and how much is sealed.

import { call } from "./api";

export interface EncryptionColumn { table: string; column: string; what: string; sealed: number; remaining: number }

export interface EncryptionReport {
  encrypted: boolean;
  dump_alone_cannot_reveal_secrets: boolean;
  root_key_holder?: { kind: string; where: string; strength: string; reachable: boolean; problem?: string };
  keys?: { current_key_encryption_key: number; data_keys: unknown[]; needs_rewrap: boolean; oldest_key_at: number };
  columns: EncryptionColumn[];
  secrets_sealed: number;
  secrets_remaining: number;
  migration: { state: string; running_on?: string; last_error?: string };
  stored_addresses_with_credentials: number;
  warnings: string[];
  advice: string[];
  not_yet_encrypted: string[];
  summary: string;
}

export const encryptionApi = { report: () => call<EncryptionReport>("GET", "/api/v1/encryption") };
