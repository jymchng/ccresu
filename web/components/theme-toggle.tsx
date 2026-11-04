"use client";

import { Moon, Sun } from "lucide-react";
import { useCallback, useSyncExternalStore } from "react";

import { Button } from "@/components/ui/button";
import {
  applyTheme,
  defaultTheme,
  themeStorageKey,
  type Theme,
} from "@/lib/theme";

/** Event the toggle dispatches after it changes the document class. */
const themeChangeEvent = "ccresu-theme-change";

function subscribe(onChange: () => void): () => void {
  window.addEventListener(themeChangeEvent, onChange);
  window.addEventListener("storage", onChange);
  return () => {
    window.removeEventListener(themeChangeEvent, onChange);
    window.removeEventListener("storage", onChange);
  };
}

/** The document element is the source of truth, because the inline bootstrap
 * script has already applied the stored preference before React runs. */
function getSnapshot(): Theme {
  return document.documentElement.classList.contains("light") ? "light" : "dark";
}

function getServerSnapshot(): Theme {
  return defaultTheme;
}

export function ThemeToggle() {
  const theme = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  const toggle = useCallback(() => {
    const next: Theme = getSnapshot() === "dark" ? "light" : "dark";
    applyTheme(next, document.documentElement);
    try {
      window.localStorage.setItem(themeStorageKey, next);
    } catch {
      // A blocked localStorage must not break the toggle.
    }
    window.dispatchEvent(new Event(themeChangeEvent));
  }, []);

  const label =
    theme === "dark" ? "Switch to light theme" : "Switch to dark theme";

  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      onClick={toggle}
      aria-label={label}
      title={label}
      data-theme={theme}
    >
      <Sun className="hidden dark:block" aria-hidden="true" />
      <Moon className="block dark:hidden" aria-hidden="true" />
    </Button>
  );
}
