import { useEffect, useState } from "react";

import { useLocalStorageState } from "./useLocalStorageState";

export const THEME_MODES = ["light", "dark", "system"] as const;
export type ThemeMode = (typeof THEME_MODES)[number];

export function isThemeMode(value: string): value is ThemeMode {
  return (THEME_MODES as readonly string[]).includes(value);
}

const THEME_MODE_STORAGE_KEY = "nebari-llm:themeMode";

function prefersDark(): boolean {
  try {
    return window.matchMedia("(prefers-color-scheme: dark)").matches;
  } catch {
    return false;
  }
}

function readStoredMode(raw: string | null): ThemeMode {
  if (raw !== null && isThemeMode(raw)) {
    return raw;
  }
  return "system";
}

/**
 * Tracks the user's theme preference (light / dark / system), persists it, and
 * toggles the `dark` class on <html> so Tailwind's dark variant applies.
 * Defaults to "system" and stays in sync with the OS preference.
 */
export function useThemePreference() {
  const [themeMode, setThemeMode] = useLocalStorageState<ThemeMode>(
    THEME_MODE_STORAGE_KEY,
    readStoredMode,
  );
  const [systemPrefersDark, setSystemPrefersDark] = useState<boolean>(prefersDark);

  // Keep "system" mode in sync with the OS preference as it changes.
  useEffect(() => {
    let mediaQuery: MediaQueryList;
    try {
      mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
    } catch {
      return;
    }

    const onChange = (event: MediaQueryListEvent) => setSystemPrefersDark(event.matches);
    mediaQuery.addEventListener("change", onChange);
    return () => mediaQuery.removeEventListener("change", onChange);
  }, []);

  const isDarkMode = themeMode === "system" ? systemPrefersDark : themeMode === "dark";

  useEffect(() => {
    document.documentElement.classList.toggle("dark", isDarkMode);
  }, [isDarkMode]);

  return { themeMode, isDarkMode, setThemeMode };
}
