import { Icon } from "../kit/Icon";

/** DoBarButton is the field in the top bar. It opens the overlay; the slash key does too. */
export function DoBarButton({ onOpen }: { onOpen: () => void }) {
  return (
    <button type="button" className="dobar" onClick={onOpen} aria-label="What do you want to do?" aria-keyshortcuts="/">
      <Icon name="search" />
      <span className="t">What do you want to do?</span>
      <kbd>/</kbd>
    </button>
  );
}
