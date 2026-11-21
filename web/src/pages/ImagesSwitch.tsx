import { onLink } from "../router";

/** The ways to look in the Images area: by image, by what is inside, or in Launchway's own registry. */
export function ImagesSwitch({ on }: { on: "images" | "components" | "registry" }) {
  return (
    <nav className="seg2" aria-label="Look for">
      <a href="/images" onClick={onLink} aria-current={on === "images" ? "page" : undefined}>Images</a>
      <a href="/images/components" onClick={onLink} aria-current={on === "components" ? "page" : undefined}>Components</a>
      <a href="/images/registry" onClick={onLink} aria-current={on === "registry" ? "page" : undefined}>Registry</a>
    </nav>
  );
}
