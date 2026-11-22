import { Component, type ReactNode } from "react";
import { BtnSm, SpanMono } from "../kit/bits";

/** What went wrong in a part of the page, in words, with a way to try again; the rest of the page stays. */
export class ErrorBoundary extends Component<{ name: string; resetKey?: string; children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) { return { error }; }

  componentDidCatch(error: Error) {
    // The browser's console keeps the stack for whoever looks.
    console.error(`${this.props.name} failed to draw:`, error);
  }

  componentDidUpdate(prev: { resetKey?: string }) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) this.setState({ error: null });
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="callout callout-err" role="alert">
        <p><strong>{this.props.name} could not be shown.</strong> Something in it failed while it was drawn: <SpanMono>{error.message || String(error)}</SpanMono>. The rest of the page works.</p>
        <p className="faint small">This is a fault in Launchway, not in your pipeline. Trying again often helps; if it keeps failing, reload the page, and report it with the text above.</p>
        <BtnSm onClick={() => this.setState({ error: null })}>Try again</BtnSm>
      </div>
    );
  }
}
