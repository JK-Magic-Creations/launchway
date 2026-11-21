// Light, dark, or follow the system. The choice is kept in the browser; where storage is not allowed
// the page still works and follows the system.

export type Theme = "light" | "dark";

const KEY = "lw-theme";

export function systemTheme(): Theme {
  return typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function currentTheme(): Theme {
  const t = document.documentElement.dataset.theme;
  return t === "dark" || t === "light" ? t : systemTheme();
}

export function setTheme(t: Theme): void {
  document.documentElement.dataset.theme = t;
  try { localStorage.setItem(KEY, t); } catch { /* storage may be blocked */ }
}

/** Applies a remembered choice at start. */
export function applyStoredTheme(): void {
  try {
    const t = localStorage.getItem(KEY);
    if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  } catch { /* storage may be blocked */ }
}
