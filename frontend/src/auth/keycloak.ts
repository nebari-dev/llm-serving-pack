import Keycloak from "keycloak-js";

import { loadAppConfig } from "@/app/config";

declare global {
  interface Window {
    // Lets non-production runs (tests, Playwright) inject a fake authenticated
    // session instead of redirecting to Keycloak. Never honored in production.
    __PW_E2E_AUTH__?: {
      authenticated: boolean;
      token?: string;
      idTokenParsed?: Record<string, string>;
    };
  }
}

let _keycloak: Keycloak | null = null;

// A stand-in Keycloak that reports an authenticated session without contacting
// a server — used by the test/E2E shim and the local-dev bypass below.
function fakeSession(token?: string, idTokenParsed?: Record<string, string>): Keycloak {
  return {
    authenticated: true,
    token,
    idTokenParsed,
    updateToken: async () => true,
    login: async () => {},
    logout: async () => {},
  } as unknown as Keycloak;
}

/**
 * Initialize Keycloak with login-required + PKCE. Resolves once the user is
 * authenticated (keycloak-js redirects to the login page if they are not).
 * Must be awaited before the app renders.
 */
export async function initKeycloak(): Promise<Keycloak> {
  if (_keycloak) {
    return _keycloak;
  }

  const injected = window.__PW_E2E_AUTH__;
  if (import.meta.env.MODE !== "production" && injected?.authenticated) {
    _keycloak = fakeSession(injected.token, injected.idTokenParsed);
    return _keycloak;
  }

  const { keycloak: cfg } = await loadAppConfig();
  const kc = new Keycloak({ url: cfg.url, realm: cfg.realm, clientId: cfg.clientId });

  await kc.init({
    onLoad: "login-required",
    pkceMethod: "S256",
    checkLoginIframe: false,
  });

  _keycloak = kc;
  return kc;
}

export function getKeycloakInstance(): Keycloak | null {
  return _keycloak;
}

/**
 * Thrown when the refresh token has expired and a full re-authentication is
 * required. Callers should stop making API calls — a redirect to Keycloak is
 * already in flight.
 */
export class SessionExpiredError extends Error {
  constructor() {
    super("Session expired — redirecting to login");
    this.name = "SessionExpiredError";
  }
}

/** Returns a valid access token, refreshing it first if it is close to expiry. */
export async function getToken(): Promise<string> {
  if (!_keycloak?.authenticated) {
    throw new SessionExpiredError();
  }

  try {
    await _keycloak.updateToken(30);
  } catch {
    _keycloak.login();
    throw new SessionExpiredError();
  }

  const token = _keycloak.token;
  if (!token) {
    _keycloak.login();
    throw new SessionExpiredError();
  }

  return token;
}

export function signOut() {
  if (_keycloak) {
    _keycloak.logout({ redirectUri: `${window.location.origin}/` });
  } else {
    window.location.href = "/";
  }
}
