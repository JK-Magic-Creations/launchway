import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "./markdown";

describe("Markdown", () => {
  it("never turns the author's text into HTML", () => {
    const { container } = render(<Markdown text={'<script>alert(1)</script> <img src=x onerror=alert(1)> **bold** and `code`'} />);
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toContain("<script>alert(1)</script>");
    expect(container.querySelector("strong")?.textContent).toBe("bold");
    expect(container.querySelector("code")?.textContent).toBe("code");
  });
  it("keeps only web and mail addresses as links, and never hands over the page", () => {
    const { container } = render(<Markdown text={"[ok](https://example.com/a) [bad](javascript:alert(1)) [mail](mailto:a@b.c)"} />);
    const links = [...container.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(links).toEqual(["https://example.com/a", "mailto:a@b.c"]);
    expect(container.querySelector("a")?.getAttribute("rel")).toContain("noopener");
  });
  it("renders blocks and shows a suggestion as a suggestion", () => {
    const { container } = render(<Markdown text={"# Title\n\n- one\n- two\n\n```suggestion\nnew line\n```\n\n> quoted"} />);
    expect(container.querySelector("h3")?.textContent).toBe("Title");
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector(".md-suggestion")?.textContent).toContain("new line");
    expect(container.querySelector("blockquote")?.textContent).toBe("quoted");
  });
});
