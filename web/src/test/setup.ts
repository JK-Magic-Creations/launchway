import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// The address is part of a page's view (see src/urlState.ts): each test starts from a clean one.
afterEach(() => { cleanup(); history.replaceState(null, "", "/"); });

// jsdom does not implement scrolling.
window.scrollTo = () => {};
