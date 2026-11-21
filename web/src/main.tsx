import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { applyStoredTheme } from "./shell/theme";
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

applyStoredTheme();
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
