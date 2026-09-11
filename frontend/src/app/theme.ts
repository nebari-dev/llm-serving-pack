/**
 * localStorage key the theme preference persists under. Predates the
 * @nebari/use-theme-preference hook, so it is passed as `storageKey` to
 * `ThemeProvider` (and baked into the index.html bootstrap script) to keep
 * users' saved preference. Keep all three in sync.
 */
export const THEME_STORAGE_KEY = "nebari-llm:themeMode";
