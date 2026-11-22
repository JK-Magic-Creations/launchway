/** What the server adds to a run's detail when the installation exports traces: the trace's id, and a link to it in the installation's trace viewer when it has one. */
export interface RunTrace { trace_id?: string; trace_url?: string }
