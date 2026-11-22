import { Dialog } from "../kit/Dialog";

const KEYS: [string, string][] = [
  ["/", "Open the finder: Flights, repositories, pipelines, Sorties, pull requests, pages and the documentation (it also shows your favorites and what you opened lately)"],
  ["?", "Show this list"],
  ["f", "On a list with an explorer: jump to its filter box"],
  ["Arrow keys, Home, End", "In a tree or a list of tabs: move"],
  ["Right, Left", "In a tree: open or close a folder"],
  ["Enter or Space", "In a tree: choose a place"],
  ["A letter", "In a tree: jump to the next place that starts with it"],
  ["Escape", "Close the finder or a dialog"],
];

/** The keyboard shortcuts, in one list. Single-key shortcuts can be turned off in the user menu. */
export default function Shortcuts({ onClose }: { onClose: () => void }) {
  return (
    <Dialog label="Keyboard shortcuts" onClose={onClose}>
      <div className="xp-dialog">
        <h2 className="h-sm">Keyboard shortcuts</h2>
        <table>
          <caption className="sr-only">Keys and what they do</caption>
          <thead><tr><th scope="col" style={{ textAlign: "left" }}>Key</th><th scope="col" style={{ textAlign: "left" }}>What it does</th></tr></thead>
          <tbody>{KEYS.map(([k, d]) => <tr key={k}><th scope="row" style={{ textAlign: "left", paddingRight: 16 }}><kbd>{k}</kbd></th><td>{d}</td></tr>)}</tbody>
        </table>
        <p className="faint small">Single-key shortcuts can be turned off in your user menu.</p>
        <div className="xp-dialog-f"><button type="button" data-autofocus className="btn" onClick={onClose}>Close</button></div>
      </div>
    </Dialog>
  );
}
