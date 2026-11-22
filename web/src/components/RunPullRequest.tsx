/** What started the run, as the server sends it; for a pull request on an outside Git host, the pull request as the host described it. */
export interface PullRequestDetail { pull_request_title?: string; pull_request_author?: string; pull_request_url?: string; pull_request_draft?: boolean; pull_request_fork?: boolean }

/** The pull request a run is for, as its Git host described it: "Pull request #N: title, by author", with a link to the
 * host. Nothing for a run that is not for a pull request or whose host said nothing. */
export function RunPullRequest({ run }: { run: { pull_request?: number; trigger_detail?: PullRequestDetail } }) {
  const d = run.trigger_detail;
  if (!run.pull_request || !d || (!d.pull_request_title && !d.pull_request_author)) return null;
  const text = `Pull request #${run.pull_request}${d.pull_request_title ? ": " + d.pull_request_title : ""}${d.pull_request_author ? ", by " + d.pull_request_author : ""}`;
  const safe = d.pull_request_url && /^https?:\/\//i.test(d.pull_request_url) ? d.pull_request_url : "";
  return (
    <p className="run-pr" aria-label="The pull request">
      {safe ? <a href={safe} rel="noreferrer noopener" target="_blank">{text}<span className="visually-hidden"> (opens on the host)</span></a> : text}
      {d.pull_request_draft ? <span className="faint"> (draft)</span> : null}
      {d.pull_request_fork ? <span className="faint"> (from a fork: restricted)</span> : null}
    </p>
  );
}
