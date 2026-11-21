import type { Needs } from "./api";

const fmtBytes = (n: number) => {
  const G = 2 ** 30, M = 2 ** 20;
  if (n >= G && n % G === 0) return `${n / G}Gi`;
  if (n >= M && n % M === 0) return `${n / M}Mi`;
  if (n >= G) return `${(n / G).toFixed(1)}Gi`;
  return `${Math.round(n / M)}Mi`;
};
const fmtCPU = (m: number) => (m % 1000 === 0 ? String(m / 1000) : `${m}m`);

/** The fields of the requirements file for one component's facts, as lines indented by `pad`. */
function fields(facts: Needs["facts"], pad: string): string[] {
  const out: string[] = [];
  const of = (facet: string) => facts.filter((f) => f.facet === facet).map((f) => f.value);
  const one = (facet: string) => of(facet)[0];
  if (one("platform")) out.push(`${pad}platform: ${one("platform")}`);
  if (of("arch").length) out.push(`${pad}architectures: [${of("arch").join(", ")}]`);
  if (one("cpu")) out.push(`${pad}cpu: ${fmtCPU(Number(one("cpu")))}`);
  if (one("memory")) out.push(`${pad}memory: ${fmtBytes(Number(one("memory")))}`);
  if (one("storage")) out.push(`${pad}storage: ${fmtBytes(Number(one("storage")))}`);
  if (of("device").length) {
    out.push(`${pad}devices:`);
    for (const d of of("device")) {
      const [kind, rest = "1"] = d.split(":");
      const [count, vendor] = rest.split("@");
      out.push(`${pad}  - { kind: ${kind}, count: ${count || 1}${vendor ? `, vendor: ${vendor}` : ""} }`);
    }
  }
  const osLines: string[] = [];
  if (one("os")) osLines.push(`${pad}  family: ${one("os")}`);
  if (one("kernel")) osLines.push(`${pad}  kernel: "${one("kernel")}"`);
  if (of("feature").length) osLines.push(`${pad}  features: [${of("feature").join(", ")}]`);
  if (osLines.length) out.push(`${pad}os:`, ...osLines);
  if (of("port").length) {
    out.push(`${pad}ports:`);
    for (const p of of("port")) out.push(`${pad}  - "${p}"`);
  }
  if (of("data").length) {
    out.push(`${pad}data:`);
    for (const d of of("data")) {
      const m = /^([a-z-]+)(.*)$/.exec(d);
      out.push(`${pad}  - { kind: ${m?.[1] ?? d}${m?.[2] ? `, version: "${m[2]}"` : ""} }`);
    }
  }
  return out;
}

/** A requirements file that says what the graph now says: a starting point to edit and propose, so nothing has to be
 * typed from nothing. What was inferred is written as if declared; the person decides what to keep. */
export function requirementsYaml(items: Needs[]): string {
  const head = ["# What the software in this repository needs. What is written here counts; what Launchway infers is only advice.", "version: 1"];
  if (items.length === 0) return [...head, "# architectures: [amd64, arm64]", "# cpu: 500m", "# memory: 512Mi", ""].join("\n");
  if (items.length === 1) return [...head, ...fields(items[0].facts, ""), ""].join("\n");
  const lines = [...head, "components:"];
  for (const it of items) {
    const dir = String(it.component.attrs?.dir ?? ".");
    const name = (dir === "." ? it.component.key.split(":")[0].split("/").pop() : dir.split("/").pop()) ?? "component";
    lines.push(`  ${name}:`, `    path: ${String(it.component.attrs?.file ?? dir)}`, ...fields(it.facts, "    "));
  }
  return [...lines, ""].join("\n");
}
