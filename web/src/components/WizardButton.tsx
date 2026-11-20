// WizardButton opens a guided set-up from the page that needs it. People
// who may not change the configuration are told who can.

import { useMe } from "../auth";
import { Link } from "./ui";

export type WizardId = "first-run" | "connect-repository" | "add-runner" | "add-environment" | "add-policy" | "add-health-source" | "add-model";

export function WizardButton({ wizard, label, primary = true, small }: { wizard: WizardId; label: string; primary?: boolean; small?: boolean }) {
  const me = useMe();
  if (!me?.admin) return null;
  return <Link href={`/config/wizard/${wizard}`} className={"btn" + (primary ? " btn-primary" : "") + (small ? " btn-sm" : "")}>{label}</Link>;
}

/** WizardEmpty is an empty state: what the thing is, in one sentence, and
 * the button that adds one (or who can, for people who may not). */
export function WizardEmpty({ title, text, wizard, label }: { title: string; text: string; wizard: WizardId; label: string }) {
  const me = useMe();
  return (
    <div className="empty">
      <div className="empty-title">{title}</div>
      <div className="empty-body">
        <p className="wiz-empty-text">{text}</p>
        {me?.admin ? <Link href={`/config/wizard/${wizard}`} className="btn btn-primary">{label}</Link>
          : <p className="faint">An administrator can add one.</p>}
      </div>
    </div>
  );
}
