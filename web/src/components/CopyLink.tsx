import { announce } from "../kit/announce";
import { BtnSm } from "../kit/bits";

/** Copies the address of the view being shown (every view has an address), and says so. */
export function CopyLink({ label = "Copy link to this view" }: { label?: string }) {
  return (
    <BtnSm onClick={() => {
      void navigator.clipboard?.writeText(location.href).then(() => announce("Link to this view copied."), () => announce("The link could not be copied; copy it from the address bar.", "assertive"));
    }}>{label}</BtnSm>
  );
}
