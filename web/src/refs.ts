// Two small helpers several pages used to carry a copy of each.

/** A branch or tag as a person says it: without refs/heads/ or refs/tags/. */
export const refName = (ref: string): string => ref.replace(/^refs\/(heads|tags)\//, "");

/** The query of the address in the bar. */
export const params = () => new URLSearchParams(location.search);
