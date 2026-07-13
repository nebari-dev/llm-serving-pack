import "@testing-library/jest-dom/vitest";

import { initKeycloak } from "@/auth/keycloak";

// jsdom under this vitest version does not expose a functional Web Storage on
// the default origin, and never implements matchMedia. Provide deterministic
// in-memory stand-ins so hooks that read theme preference work under test.

class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length() {
    return this.store.size;
  }
  clear() {
    this.store.clear();
  }
  getItem(key: string) {
    return this.store.has(key) ? (this.store.get(key) as string) : null;
  }
  key(index: number) {
    return Array.from(this.store.keys())[index] ?? null;
  }
  removeItem(key: string) {
    this.store.delete(key);
  }
  setItem(key: string, value: string) {
    this.store.set(key, String(value));
  }
}

Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: new MemoryStorage(),
});

if (typeof window.matchMedia !== "function") {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  });
}

// Inject a fake authenticated Keycloak session so api.ts can attach a bearer
// token without redirecting to a real Keycloak. initKeycloak() honors this shim
// outside production builds.
window.__PW_E2E_AUTH__ = {
  authenticated: true,
  token: "test-token",
  idTokenParsed: { name: "Test User", email: "test@example.com", preferred_username: "test" },
};
await initKeycloak();
