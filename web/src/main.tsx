import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { applyStoredTheme } from "./shell/theme";
import { loadVocabulary } from "./terms";
import "./styles/fonts.css";
import "./styles/tokens.css";
import "./styles/shell.css";
import "./styles/components.css";
import "./styles/kit.css";
import "./styles/runs.css";
import "./styles/projects.css";
import "./styles/editor.css";
import "./styles/access.css";
import "./styles/pipelines.css";
import "./styles/tests.css";
import "./styles/deployments.css";
import "./styles/security.css";
import "./styles/reports.css";
import "./styles/verification.css";
import "./styles/guided.css";
import "./styles/v2b.css";
import "./styles/words.css";
import "./styles/a11y.css";

applyStoredTheme();
const start = () => createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
// The words come first (a small separate file), so no page is ever drawn with a placeholder; the overview
// replaces them with the server's own table once it is read.
loadVocabulary().catch(() => undefined).finally(start);
