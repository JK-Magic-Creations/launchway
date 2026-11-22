// What a person must be told about each kind of outside Git host: its name, what it calls a change request, whether
// its address must be given, and the steps to set up its webhook. The pages are the same for every kind; only
// these words differ. A kind that is not listed here still works and is shown by its kind name.

export interface HostKind {
  value: string;
  /** The host's product name. */
  name: string;
  /** What the host calls a change request, lower case. */
  change: string;
  /** The address of an install is needed (there is no public service to assume). */
  addressRequired?: boolean;
  /** The webhook form's name for the secret, and whether its content type must be chosen. */
  secretField: string;
  json?: boolean;
}

export const HOST_KINDS: HostKind[] = [
  { value: "github", name: "GitHub", change: "pull request", secretField: "Secret", json: true },
  { value: "gitlab", name: "GitLab", change: "merge request", secretField: "Secret token" },
];

/** The kind's label in the picker. */
export const kindLabel = (k: HostKind) => `${k.name}${k.addressRequired ? ", self-managed" : ", public or self-managed"}`;

/** The steps of the host's webhook form. */
export const webhookSteps = (k?: HostKind) => [
  ...(k?.json ? ["Content type application/json."] : []),
  `The secret you stored for this connection goes in ${k?.secretField ?? "its secret field"}.`,
  `Events: pushes, tag pushes and ${k?.change ?? "pull request"}s.`,
];

export function hostKind(kind: string | undefined): HostKind | undefined {
  return HOST_KINDS.find((k) => k.value === kind);
}
