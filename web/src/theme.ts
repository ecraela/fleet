export type Theme = "light" | "dark";
const key = "haproxy-manager-theme";

export function readTheme(): Theme {
  try {
    const saved = localStorage.getItem(key);
    if (saved === "light" || saved === "dark") return saved;
  } catch {
    // Appearance still works when browser storage is unavailable.
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function saveTheme(theme: Theme) {
  try {
    localStorage.setItem(key, theme);
  } catch {
    /* A theme preference does not block local login. */
  }
}
