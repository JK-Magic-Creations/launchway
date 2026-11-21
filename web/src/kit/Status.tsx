import type { ReactNode } from "react";
import type { RunStatus, StepStatus } from "../api";
import { Icon, type IconName } from "./Icon";
import { statusLabel } from "../components/ui";

/** The states the design knows. A state is a shape, a colour and a word, never colour alone. */
export type State = "ok" | "bad" | "warn" | "wait" | "run" | "idle" | "skip";

const WORD: Record<State, string> = {
  ok: "Passed", bad: "Failed", warn: "Needs a look", wait: "Waiting for a person", run: "Running", idle: "Not started", skip: "Skipped",
};
const GLYPH: Partial<Record<State, IconName>> = { ok: "check", bad: "x", warn: "alert", wait: "pause" };

/** Status is the small disc: a check, a cross, pause bars, a spinning arc, a dashed ring or a plain ring. */
export function Status({ state, size, label }: { state: State; size?: "l"; label?: string }) {
  const g = GLYPH[state];
  return (
    <span className={["st", state, size].filter(Boolean).join(" ")} role="img" aria-label={label ?? (state === "run" ? statusLabel("running") : WORD[state])}>
      {g && <Icon name={g} />}
    </span>
  );
}

/** The design's state for a step or a run. */
export function stateOf(s: StepStatus | RunStatus): State {
  switch (s) {
    case "succeeded": return "ok";
    case "failed": return "bad";
    case "running": case "leased": return "run";
    case "waiting": return "wait";
    case "skipped": case "canceled": return "skip";
    default: return "idle";
  }
}

/** The design's state for a launch-line station. */
export function stateOfStation(s: string): State {
  switch (s) {
    case "done": return "ok";
    case "failed": return "bad";
    case "running": return "run";
    case "waiting": return "wait";
    case "skipped": return "skip";
    default: return "idle";
  }
}

export type Tone = "ok" | "warn" | "bad" | "run";

/** Chip is a small label: neutral, or ok, warn, bad or running, with an optional icon. */
export function Chip({ tone, icon, children }: { tone?: Tone; icon?: IconName; children: ReactNode }) {
  return <span className={["vchip", tone].filter(Boolean).join(" ")}>{icon && <Icon name={icon} size="s" />}{children}</span>;
}

/** StateChip is a chip that names a state in the design's words. */
export function StateChip({ state, children }: { state: State; children?: ReactNode }) {
  const tone: Tone | undefined = state === "ok" ? "ok" : state === "bad" ? "bad" : state === "wait" || state === "warn" ? "warn" : state === "run" ? "run" : undefined;
  const icon: IconName | undefined = GLYPH[state];
  return <Chip tone={tone} icon={icon}>{children ?? WORD[state]}</Chip>;
}
