// TypeScript/JavaScript kit for Launchway custom steps: a small wrapper of the
// step contract (docs/reference/step-contract.md). No dependencies, and only
// syntax Node can run by stripping types (Node 22.6 or newer), so a step can
// be run as `node main.ts`. Copy this file into a step (`launchway step new
// --lang ts` does) or import it from @launchway/toggles/step.
//
//   import { run } from "./launchway-step.ts";
//   run((step) => {
//     const who = step.string("who");
//     step.output("greeting", `Hello, ${who}`);
//     step.summary(`Greeted **${who}**`);
//   });
//
// Throw `new Failed(msg)`, `new Allowed(msg)` or `new NeedsPerson(question)`
// for exit codes 1, 64 and 65.

import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import { env, exit } from "node:process";

export const CONTRACT = 1;

export class Failed extends Error {
  code = 1;
}
export class Allowed extends Error {
  code = 64;
}
export class NeedsPerson extends Error {
  code = 65;
}

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

export class Step {
  private inputs: Record<string, Json>;
  private outputs: Record<string, Json> = {};
  constructor() {
    const path = env.LAUNCHWAY_STEP_INPUTS;
    if (!path) throw new Failed("LAUNCHWAY_STEP_INPUTS is not set: run this step with `launchway step test`, or in a pipeline");
    const doc = JSON.parse(readFileSync(path, "utf8"));
    if ((doc.contract ?? 1) > CONTRACT) throw new Failed(`the Station speaks contract ${doc.contract}; this kit speaks ${CONTRACT}: update the kit`);
    this.inputs = doc.inputs ?? {};
  }
  has(name: string): boolean {
    return name in this.inputs;
  }
  private get<T>(name: string, type: string, fallback: T): T {
    if (!(name in this.inputs)) return fallback;
    const v = this.inputs[name];
    const ok = type === "array" ? Array.isArray(v) : type === "object" ? v !== null && typeof v === "object" && !Array.isArray(v) : typeof v === type;
    if (!ok) throw new Failed(`input ${name} is not a ${type}`);
    return v as T;
  }
  string(name: string): string {
    return this.get(name, "string", "");
  }
  number(name: string): number {
    return this.get(name, "number", 0);
  }
  boolean(name: string): boolean {
    return this.get(name, "boolean", false);
  }
  list(name: string): string[] {
    return this.get<string[]>(name, "array", []);
  }
  record(name: string): Record<string, Json> {
    return this.get<Record<string, Json>>(name, "object", {});
  }
  /** The value of a secret input, which the Station put in an environment variable. */
  secret(name: string): string {
    const ref = this.inputs[name] as { secret?: string } | undefined;
    if (!ref || typeof ref !== "object" || !ref.secret) throw new Failed(`input ${name} is not a secret reference`);
    return env[ref.secret] ?? "";
  }
  output(name: string, value: Json): void {
    this.outputs[name] = value;
  }
  secretOutput(name: string, value: string): void {
    this.mask(value);
    this.output(name, value);
  }
  writeOutputs(): void {
    const path = env.LAUNCHWAY_STEP_OUTPUTS;
    if (path && Object.keys(this.outputs).length > 0) writeFileSync(path, JSON.stringify(this.outputs));
  }
  private result(r: Record<string, Json>): void {
    const path = env.LAUNCHWAY_STEP_RESULTS;
    if (path) appendFileSync(path, JSON.stringify(r) + "\n");
  }
  summary(markdown: string): void {
    this.result({ t: "summary", markdown });
  }
  annotate(level: "note" | "warning" | "error", file: string, line: number, message: string): void {
    this.result({ t: "annotation", level, file, line, message });
  }
  finding(rule: string, severity: "critical" | "high" | "medium" | "low", file: string, line: number, message: string): void {
    this.result({ t: "finding", rule, severity, file, line, message });
  }
  test(name: string, status: "passed" | "failed" | "skipped", millis = 0, message = ""): void {
    this.result({ t: "test", name, status, duration_ms: Math.round(millis), message });
  }
  mask(value: string): void {
    this.result({ t: "mask", value });
  }
  needsPerson(question: string): void {
    this.result({ t: "needs_person", message: question });
  }
}

/** The environment without the variables that let a program write results and
 *  outputs: pass it as `env` to the programs the step starts (a build tool, a
 *  scanner), so that nothing they run can forge a result. */
export function environ(): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = { ...env };
  delete out.LAUNCHWAY_STEP_RESULTS;
  delete out.LAUNCHWAY_STEP_OUTPUTS;
  return out;
}

/** Open the step, run fn(step), write the outputs and exit with the code the result means. */
export function run(fn: (step: Step) => void | Promise<void>): void {
  let step: Step;
  try {
    step = new Step();
  } catch (e) {
    console.error("launchway step:", (e as Error).message);
    exit(1);
  }
  Promise.resolve()
    .then(() => fn(step))
    .then(
      () => {
        step.writeOutputs();
        exit(0);
      },
      (e) => {
        const code = (e as { code?: number }).code;
        if (e instanceof NeedsPerson) step.needsPerson(e.message);
        console.error(e instanceof NeedsPerson ? "needs a person: " + e.message : (e as Error).message ?? String(e));
        step.writeOutputs();
        exit(code === 64 || code === 65 ? code : 1);
      },
    );
}
