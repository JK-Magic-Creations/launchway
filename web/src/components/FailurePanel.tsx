import type { ReactNode } from "react";
import type { Failure } from "../guided/derive";
import { Icon } from "../kit/Icon";
import { StepCauseView } from "./StepCause";
import { terms } from "../terms";

/** FailurePanel says in plain words what went wrong in a failed run and what can be done about it.
 * It is built from what the server recorded: the failed step, its exit code, how it ended, and its last
 * lines of output. The actions are the page's own (resume, re-run, open the log). */
export function FailurePanel({ failure, actions }: { failure: Failure; actions: ReactNode }) {
  return (
    <section className="wrong" aria-labelledby="explain-h">
      <div className="wrong-h">
        <span className="ico-tile bad"><Icon name="alert" /></span>
        <div>
          <h2 id="explain-h">What went wrong</h2>
          <p>{failure.headline}</p>
          {failure.others > 0 && <p className="faint">{failure.others === 1 ? "One more step also failed." : `${failure.others} more steps also failed.`} The first one is explained here.</p>}
        </div>
      </div>
      <div className="wrong-b">
        <div>
          <h3 className="h-sm">What you can do</h3>
          <ul className="explain-advice">{failure.advice.map((a) => <li key={a}>{a}</li>)}</ul>
          <p className="faint small">{failure.cause ? "Re-running from the failed step makes a new run at the same commit: the steps before it are reused, and nothing is rebuilt." : terms().say("Running again from the failed step keeps the steps that passed and stays in this {run}. Running everything again starts a new run at the same commit.")}</p>
          <div className="explain-actions">{actions}</div>
        </div>
        <div>
          {failure.cause ? <StepCauseView cause={failure.cause} summary={false} /> : failure.excerpt ? (
            <figure className="excerpt">
              <figcaption>The last lines {failure.step} wrote{failure.exitCode !== null ? ` before exit code ${failure.exitCode}` : ""}:</figcaption>
              <pre>{failure.excerpt}</pre>
            </figure>
          ) : <p className="faint">{failure.step} wrote nothing before it stopped.</p>}
        </div>
      </div>
    </section>
  );
}
