import type { ReactNode } from "react";
import type { Failure } from "../guided/derive";

/** FailurePanel says in plain words what went wrong in a failed run and what can be done about it.
 * It is built from what the server recorded: the failed step, its exit code, how it ended, and its last
 * lines of output. The actions are the page's own (resume, re-run, open the log). */
export function FailurePanel({ failure, actions }: { failure: Failure; actions: ReactNode }) {
  return (
    <section className="explain" aria-labelledby="explain-h">
      <h2 id="explain-h" className="explain-title">What went wrong</h2>
      <p className="explain-headline">{failure.headline}</p>
      {failure.others > 0 && <p className="faint">{failure.others === 1 ? "One more step also failed." : `${failure.others} more steps also failed.`} The first one is explained here.</p>}
      <h3 className="h-sm">What you can do</h3>
      <ul className="explain-advice">{failure.advice.map((a) => <li key={a}>{a}</li>)}</ul>
      <p className="faint">Steps that succeeded are kept: resuming runs only what did not finish, at the same commit.</p>
      {failure.excerpt && (
        <figure className="excerpt">
          <figcaption>The last lines {failure.step} wrote{failure.exitCode !== null ? ` before exit code ${failure.exitCode}` : ""}:</figcaption>
          <pre>{failure.excerpt}</pre>
        </figure>
      )}
      <div className="explain-actions">{actions}</div>
    </section>
  );
}
