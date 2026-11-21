import { onLink } from "../router";

/** The two ways to look in the Images area: by image, or by what is inside. */
export function ImagesSwitch({ on }: { on: "images" | "components" }) {
  return (
    <nav className="seg2" aria-label="Look for">
      <a href="/images" onClick={onLink} aria-current={on === "images" ? "page" : undefined}>Images</a>
      <a href="/images/components" onClick={onLink} aria-current={on === "components" ? "page" : undefined}>Components</a>
    </nav>
  );
}
