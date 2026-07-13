// Runtime configuration loaded from /config.json at startup. The file is
// rendered by the Helm chart (values.yaml → frontend.keycloak.*) and mounted
// into the nginx container, so realm/clientId change without a rebuild.
//
// Call loadAppConfig() once before the app renders (see main.tsx); afterwards
// use getAppConfig() to read the cached value.

export type AppConfig = {
  keycloak: { url: string; realm: string; clientId: string };
  /** Optional page-title override shown in the browser tab. */
  title?: string;
};

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
  _config = (await res.json()) as AppConfig;
  return _config;
}

/** Returns the cached config, or null if loadAppConfig() has not yet resolved. */
export function getAppConfig(): AppConfig | null {
  return _config;
}
