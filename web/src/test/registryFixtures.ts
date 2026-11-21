import type { ProjectRegistry, RegistryImage, RegistryStatus } from "../registryApi";

/** The built-in registry: its status, one project's repositories and one repository's images (a tagged, signed and
 * scanned image, a multi-platform index and an untagged one), and a policy with a retention rule. */
export function registryFixtures(): Record<string, unknown> {
  const status: RegistryStatus = {
    enabled: true, address: "https://registry.example.com", host: "registry.example.com", insecure: false, store: "database", repositories: 2,
    login: "docker login registry.example.com   (user: anything; password: a Launchway API token)",
    encryption: { blobs_encrypted_by_launchway: false, note: "Launchway does not encrypt the registry's blobs itself in this release: encrypt the volume or database that holds the artifact store." },
    limits: { max_blob_bytes: 10 * 2 ** 30, max_manifest_bytes: 4 * 2 ** 20, default_quota_bytes: 20 * 2 ** 30, requests_per_minute: 3000, gc_grace_seconds: 3600 },
  };
  const effective = { quota_bytes: 20 * 2 ** 30, quota_from: "default", retention: { keep_last: 5, keep_tags: ["v*"], untagged_days: 7 }, retention_from: "project", has_retention: true, immutable_tags: ["v*"], pull_from: [] };
  const project: ProjectRegistry = {
    project: "shop-api", host: status.host, insecure: false, used_bytes: 3 * 2 ** 30, quota_bytes: 20 * 2 ** 30, quota_from: "default", policy: effective,
    push_example: "registry.example.com/shop-api/<image>:<tag>",
    repositories: [{ name: "shop-api/web", images: 3, tags: 4, bytes: 2 ** 30, pushed_at: 1_700_000_000_000 }, { name: "shop-api/charts/shop", images: 1, tags: 1, bytes: 4096, pushed_at: 1_700_000_100_000 }],
  };
  const digest = (c: string) => "sha256:" + c.repeat(64);
  const images: RegistryImage[] = [
    { digest: digest("a"), media_type: "application/vnd.oci.image.manifest.v1+json", tags: ["1.4.0", "latest"], total_size: 80 * 2 ** 20, platforms: [], created_at: 1_700_000_000_000, created_by: "run run_7", run_id: "run_7",
      signed: true, has_sbom: true, has_provenance: true, vulnerabilities: { critical: 0, high: 2, medium: 5, low: 9, scanner: "scanner" }, pull: "registry.example.com/shop-api/web:1.4.0" },
    { digest: digest("b"), media_type: "application/vnd.oci.image.index.v1+json", tags: ["1.3.0"], total_size: 160 * 2 ** 20, platforms: ["linux/amd64", "linux/arm64"], created_at: 1_699_000_000_000, created_by: "ann",
      signed: false, has_sbom: false, has_provenance: false, pull: "registry.example.com/shop-api/web:1.3.0" },
    { digest: digest("c"), media_type: "application/vnd.oci.image.manifest.v1+json", tags: [], total_size: 2048, created_at: 1_698_000_000_000, created_by: "ann", signed: false, has_sbom: false, has_provenance: false,
      pull: "registry.example.com/shop-api/web@" + digest("c") },
  ];
  return {
    "/api/v1/registry": status,
    "/api/v1/projects/shop-api/registry": project,
    "/api/v1/projects/shop-api/registry/images": { repository: "shop-api/web", images },
    "/api/v1/projects/shop-api/registry/retention": { dry_run: true, candidates: [{ repository: "shop-api/web", digest: digest("c"), tags: [], reason: "untagged and older than 7 days", size: 2048, created_at: 1_698_000_000_000, attached: 0 }] },
    "/api/v1/projects/shop-api/registry/policy": { scope: "project", name: "shop-api", set: true, policy: {}, effective },
  };
}
