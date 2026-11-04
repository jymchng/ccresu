export type Theme = "dark" | "light";

/** localStorage key shared by the inline bootstrap script and the toggle. */
export const themeStorageKey = "ccresu-theme";

/** The site is dark-first: a first-time visitor gets the dark palette. */
export const defaultTheme: Theme = "dark";

/** Parse a stored value, falling back to the default for anything unexpected. */
export function parseTheme(value: string | null | undefined): Theme {
  return value === "light" || value === "dark" ? value : defaultTheme;
}

/** Read the stored preference from a Storage-like object. */
export function readStoredTheme(
  storage: Pick<Storage, "getItem"> | null | undefined,
): Theme {
  if (!storage) {
    return defaultTheme;
  }
  try {
    return parseTheme(storage.getItem(themeStorageKey));
  } catch {
    return defaultTheme;
  }
}

/** Apply a theme to the document element, replacing the other class. */
export function applyTheme(theme: Theme, root: HTMLElement): void {
  root.classList.remove("light", "dark");
  root.classList.add(theme);
  root.style.colorScheme = theme;
}

/**
 * Runs before first paint so a stored light preference does not flash dark.
 * Kept as a string because it must be inlined into <head> as-is.
 */
export const themeBootstrapScript = `(function(){try{var k="${themeStorageKey}";var s=window.localStorage.getItem(k);var t=s==="light"?"light":"dark";var r=document.documentElement;r.classList.remove("light","dark");r.classList.add(t);r.style.colorScheme=t;}catch(e){}})();`;
