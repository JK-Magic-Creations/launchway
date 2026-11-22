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
  /** The webhook form's name for the secret (default "Secret"), and whether it has no content type to choose. */
  secretField?: string;
  noType?: boolean;
}

export const HOST_KINDS: HostKind[] = [
  { value: "github", name: "GitHub", change: "pull request" },
  { value: "gitlab", name: "GitLab", change: "merge request", secretField: "Secret token", noType: true },
  { value: "gitea", name: "Gitea", change: "pull request", addressRequired: true },
  { value: "forgejo", name: "Forgejo", change: "pull request", addressRequired: true },
];

/** The steps of the host's webhook form. */
export const webhookSteps = (k?: HostKind) => [
  ...(k?.noType ? [] : ["Content type application/json."]),
  `The secret you stored for this connection goes in ${k?.secretField ?? "Secret"}.`,
  `Events: pushes, tag pushes and ${k?.change ?? "pull request"}s.`,
];

export function hostKind(kind: string | undefined): HostKind | undefined {
  return HOST_KINDS.find((k) => k.value === kind);
}
