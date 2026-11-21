import { Icon } from "../kit/Icon";
import { locate } from "../nav/model";
import { onLink, usePath } from "../router";
import { keyOnLine, learnFor, referenceFor, targetHref, type LearnTarget } from "./links";

/** A link into the documentation that ships with this release: "Learn more about approvals". */
export function LearnLink({ to, className }: { to: LearnTarget; className?: string }) {
  return (
    <a className={"learn-link" + (className ? " " + className : "")} href={targetHref(to)} onClick={onLink} data-learn={to.slug}>
      <Icon name="book" size="s" />{to.label}
    </a>
  );
}

/** The Learn link for the page being shown, if it has one. */
export function PageLearn({ className }: { className?: string }) {
  const path = usePath();
  const at = locate(path);
  const target = at ? learnFor(at.info.pattern, path) : null;
  return target ? <LearnLink to={target} className={className} /> : null;
}

/** The reference for the pipeline key on the line the editor's cursor is on: "Reference: needs". */
export function KeyHelp({ line }: { line: string }) {
  const target = referenceFor(keyOnLine(line));
  return target ? <LearnLink to={target} className="key-help" /> : null;
}
