// Runtime configuration loaded from /config.json at startup. The file is
// rendered by the Helm chart (values.yaml → frontend.keycloak.* and
// frontend.branding.*) and mounted into the nginx container, so realm/clientId
// and branding change without a rebuild.
//
// Outside Kubernetes the same /config.json is served statically (the copy baked
// into the image, a file mounted over it, or one generated at container start
// from BRANDING_* env vars — see frontend/docker-entrypoint.sh). Branding
// resolves per field with this precedence:
//   chart-rendered config.json → local config.json file → BRANDING_* env vars
//   → built-in Nebari defaults (index.html title/favicon, bundled logos,
//   index.css theme tokens).
//
// Call loadAppConfig() once before the app renders (see main.tsx), then
// applyAppConfig() to apply branding ahead of the first paint; afterwards use
// getAppConfig() to read the cached value.

/**
 * Theme token overrides keyed by the camelCase token name (e.g.
 * `primaryForeground`). Applied at runtime as the kebab-case CSS custom
 * property `--primary-foreground`, scoped to `:root` (light) or `.dark`.
 * Mirrors the tokens documented as overridable in the chart's values.yaml.
 */
export type ThemeTokens = Partial<
  Record<
    | "primary"
    | "primaryForeground"
    | "background"
    | "foreground"
    | "secondary"
    | "secondaryForeground"
    | "muted"
    | "mutedForeground"
    | "accent"
    | "accentForeground"
    | "border"
    | "ring"
    | "radius",
    string
  >
>;

export type Branding = {
  /** URL to a custom logo used in the header (light mode / default). */
  logoUrl?: string;
  /** URL to a custom dark-mode logo; falls back to logoUrl, then the built-in. */
  logoUrlDark?: string;
  /** URL to a custom favicon. */
  faviconUrl?: string;
  /** CSS variable overrides applied at runtime for light and dark modes. */
  theme?: { light?: ThemeTokens; dark?: ThemeTokens };
};

export type AppConfig = {
  keycloak: { url: string; realm: string; clientId: string };
  /** Optional page-title override shown in the browser tab. */
  title?: string;
} & Branding;

// Block CSS injection vectors: rule terminators, braces, HTML chars, quotes,
// backslashes, and url()/expression()/javascript: functions. A token value
// containing any of these is dropped rather than applied.
const UNSAFE_CSS = /[;<>{}"'\\]|url\s*\(|expression\s*\(|javascript:/i;

/** Returns the value unchanged if it is a safe CSS token, otherwise undefined. */
export function safeCssValue(value: string | undefined): string | undefined {
  return value && !UNSAFE_CSS.test(value) ? value : undefined;
}

// Accept only non-empty, well-formed http(s) URLs or root-relative paths;
// anything else (including "") becomes undefined so a bad config value can't
// land in an <img src> or <link href>.
function sanitizeUrl(value: string | undefined): string | undefined {
  if (!value) {
    return undefined;
  }
  if (value.startsWith("/")) {
    return value;
  }
  try {
    const { protocol } = new URL(value);
    return protocol === "http:" || protocol === "https:" ? value : undefined;
  } catch {
    return undefined;
  }
}

let _config: AppConfig | null = null;

/** Fetch and cache /config.json. The network request happens at most once. */
export async function loadAppConfig(): Promise<AppConfig> {
  if (_config) {
    return _config;
  }
  const res = await fetch("/config.json");
  if (!res.ok) {
    throw new Error(`Failed to load /config.json: ${res.status}`);
  }
  const config = (await res.json()) as AppConfig;
  // Drop malformed logo/favicon URLs (defence-in-depth, mirroring the
  // theme-token sanitisation applied in applyAppConfig).
  config.logoUrl = sanitizeUrl(config.logoUrl);
  config.logoUrlDark = sanitizeUrl(config.logoUrlDark);
  config.faviconUrl = sanitizeUrl(config.faviconUrl);
  _config = config;
  return _config;
}

/** Returns the cached config, or null if loadAppConfig() has not yet resolved. */
export function getAppConfig(): AppConfig | null {
  return _config;
}

const toKebab = (s: string) => s.replace(/([A-Z])/g, "-$1").toLowerCase();

/** Renders a token map to CSS declarations, dropping empty/unsafe values. */
function toCssVars(tokens: ThemeTokens): string {
  return Object.entries(tokens)
    .map(([k, v]) => [k, safeCssValue(v)] as const)
    .filter((entry): entry is readonly [string, string] => entry[1] !== undefined)
    .map(([k, v]) => `  --${toKebab(k)}: ${v};`)
    .join("\n");
}

/**
 * Applies branding from /config.json to the document before React mounts:
 * page title, favicon, and theme token overrides. Each field falls back to the
 * built-in Nebari default when unset, so an all-empty branding block (the
 * default) leaves the app visually identical to before.
 *
 * Theme overrides are injected as a <style> appended last to <head> so they win
 * the cascade over the base tokens defined in index.css.
 */
export function applyAppConfig(config: AppConfig): void {
  if (config.title) {
    document.title = config.title;
  }

  if (config.faviconUrl) {
    const link = (document.querySelector("link[rel~='icon']") ??
      Object.assign(document.createElement("link"), { rel: "icon" })) as HTMLLinkElement;
    link.href = config.faviconUrl;
    document.head.appendChild(link);
  }

  if (config.theme) {
    let css = "";
    if (config.theme.light) {
      const vars = toCssVars(config.theme.light);
      if (vars) {
        css += `:root {\n${vars}\n}\n`;
      }
    }
    if (config.theme.dark) {
      const vars = toCssVars(config.theme.dark);
      if (vars) {
        css += `.dark {\n${vars}\n}\n`;
      }
    }
    if (css) {
      const style = document.createElement("style");
      style.setAttribute("data-branding", "");
      style.textContent = css;
      document.head.appendChild(style);
    }
  }
}
