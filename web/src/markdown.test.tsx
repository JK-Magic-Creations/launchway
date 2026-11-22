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

  it("renders a table: a header row, a divider row, the body, alignment, escaped pipes and inline Markdown in cells", () => {
    const text = "Intro\n| Name | Count | Note |\n|:---|---:|:---:|\n| `a|b` | **3** | x \\| y |\n| second | 4 |\n\nAfter the table.";
    const { container } = render(<Markdown text={text} />);
    const t = container.querySelector("table")!;
    expect([...t.querySelectorAll("th")].map((c) => c.textContent)).toEqual(["Name", "Count", "Note"]);
    expect(t.querySelectorAll("th")[0].getAttribute("scope")).toBe("col");
    const rows = [...t.querySelectorAll("tbody tr")];
    expect(rows).toHaveLength(2);
    expect([...rows[0].querySelectorAll("td")].map((c) => c.textContent)).toEqual(["a|b", "3", "x | y"]);
    expect(rows[0].querySelector("code")?.textContent).toBe("a|b");
    expect(rows[0].querySelector("strong")?.textContent).toBe("3");
    expect([...rows[1].querySelectorAll("td")].map((c) => c.textContent)).toEqual(["second", "4", ""]);
    expect(t.querySelectorAll("th")[1].className).toBe("md-right");
    expect(t.querySelectorAll("th")[2].className).toBe("md-center");
    expect(container.textContent).toContain("Intro");
    expect(container.textContent).toContain("After the table.");
    // A scrolling table can be reached and read with the keyboard.
    expect(container.querySelector(".md-table")?.getAttribute("tabindex")).toBe("0");
  });

  it("leaves a line with pipes that is not a table as text", () => {
    const { container } = render(<Markdown text={"a | b | c\nstill the same paragraph\n\nx | y\n--- \nnot a divider row of the same width"} />);
    expect(container.querySelector("table")).toBeNull();
    expect(container.textContent).toContain("a | b | c");
  });
});
