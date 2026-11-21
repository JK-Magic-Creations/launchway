import { announce } from "../kit/announce";

/** Copies the address of the view being shown (every view has an address), and says so. */
export function CopyLink({ label = "Copy link to this view" }: { label?: string }) {
  return (
    <button type="button" className="btn btn-sm" onClick={() => {
      void navigator.clipboard?.writeText(location.href).then(() => announce("Link to this view copied."), () => announce("The link could not be copied; copy it from the address bar.", "assertive"));
    }}>{label}</button>
  );
}
