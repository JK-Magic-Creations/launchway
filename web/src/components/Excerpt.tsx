import { type StepView } from "../api";

// Excerpt shows the last lines a failed step wrote, as the runner kept them
// (secrets masked).
export function Excerpt({ step }: { step: StepView }) {
  return (
    <figure className="excerpt">
      <figcaption>The last lines {step.id} wrote{step.exit_code !== null && step.exit_code !== undefined ? ` before exit code ${step.exit_code}` : ""}:</figcaption>
      <pre>{step.excerpt}</pre>
    </figure>
  );
}
