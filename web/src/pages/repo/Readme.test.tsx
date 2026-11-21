import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Markdown } from "../../markdown";
import { resolveInside } from "./Readme";

const ctx = { link: (r: string) => { const p = resolveInside("docs", r); return p === null ? null : "/code?path=" + p; }, image: (r: string) => { const p = resolveInside("docs", r); return p ? "/raw?path=" + p : null; } };

describe("a README", () => {
  it("resolves relative addresses inside the repository and never outside it", () => {
    expect(resolveInside("docs", "../a.md")).toBe("a.md");
    expect(resolveInside("docs", "./b/c.md")).toBe("docs/b/c.md");
    expect(resolveInside("", "/x/y")).toBe("x/y");
    expect(resolveInside("docs", "../../etc/passwd")).toBeNull();
  });
  it("shows markup as text, drops unsafe links and keeps pictures inside an image element", () => {
    const { container } = render(<Markdown ctx={ctx} text={'<script>alert(1)</script> <img src=x onerror=alert(1)>\n\n[bad](javascript:alert(1)) [out](../../x) [ok](a.md) ![p](pic.png) ![e](https://evil.example/t.png) [w](https://example.com)'} />);
    expect(container.querySelector("script")).toBeNull();
    expect(container.innerHTML).not.toContain("javascript:");
    expect(container.querySelectorAll("img").length).toBe(1);
    expect(container.querySelector("img")?.getAttribute("src")).toBe("/raw?path=docs/pic.png");
    expect(container.textContent).toContain("<script>");
    const hrefs = [...container.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["/code?path=docs/a.md", "https://example.com"]);
  });
});
