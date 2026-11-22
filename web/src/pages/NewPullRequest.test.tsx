import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NewPullRequestPage } from "./PullRequests";

function serve(template: string, path = ".launchway/pull_request_template.md") {
  const urls: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    urls.push(url);
    if (url.includes("/pull-template")) return new Response(JSON.stringify({ path: template ? path : "", template }), { status: 200 });
    return new Response(JSON.stringify({ base: "main", head: "feat", ahead: 1, behind: 0, commits: [{ sha: "abc", subject: "Add it" }], changes: { files: [], total: 0, additions: 0, deletions: 0 }, mergeability: { state: "clean" } }), { status: 200 });
  }));
  return urls;
}
afterEach(() => vi.unstubAllGlobals());

describe("the description of a new pull request", () => {
  it("starts from the repository's template and says where it came from", async () => {
    const urls = serve("## Why\n\n## Testing\n");
    history.replaceState(null, "", "/repositories/acme/shop/pulls/new?head=feat&base=main");
    render(<main><NewPullRequestPage path="acme/shop" /></main>);
    const box = (await screen.findByLabelText(/Description/)) as HTMLTextAreaElement;
    await vi.waitFor(() => expect(box.value).toBe("## Why\n\n## Testing\n"));
    expect(screen.getByText(".launchway/pull_request_template.md")).toBeTruthy();
    expect(urls.some((u) => u.endsWith("/pull-template?ref=main"))).toBe(true);
  });
  it("leaves what the person wrote alone and has nothing to say for a repository with no template", async () => {
    serve("");
    history.replaceState(null, "", "/repositories/acme/shop/pulls/new?head=feat");
    render(<main><NewPullRequestPage path="acme/shop" /></main>);
    const box = (await screen.findByLabelText(/Description/)) as HTMLTextAreaElement;
    await userEvent.type(box, "my words");
    expect(box.value).toBe("my words");
    expect(screen.queryByText(/Started from/)).toBeNull();
  });
});
