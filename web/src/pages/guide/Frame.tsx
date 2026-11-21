import type { ReactNode } from "react";
import { LearnLink } from "../../learn/LearnLink";
import type { LearnTarget } from "../../learn/links";
import { terms } from "../../terms";
import type { GuideAction } from "../../guideApi";

/** The helper's words for a step, in the installation's own word for it (Wingman, or Guide), and the link into Learn. */
export function Wingman({ children, learn }: { children: ReactNode; learn: LearnTarget }) {
  return (
    <aside className="gd-wingman" aria-label={terms().label("guide")}>
      <div className="gd-wingman-who">{terms().label("guide")}</div>
      <div>{children}</div>
      <LearnLink to={learn} />
    </aside>
  );
}

/** What cannot be done now, with the plain reason and what would help: shown in place of a button that would fail. */
export function Unavailable({ action }: { action: GuideAction | undefined }) {
  if (!action || action.available) return null;
  return (
    <div className="callout callout-info" role="note">
      <strong>Not available here.</strong> {action.reason}{action.would_help ? ` ${action.would_help}` : ""}
    </div>
  );
}

export function Row({ children }: { children: ReactNode }) { return <div className="gd-row">{children}</div>; }
