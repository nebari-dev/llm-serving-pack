import { afterEach, describe, expect, it, vi } from "vitest";

import { type AppConfig, applyAppConfig, safeCssValue } from "./config";

function baseConfig(overrides: Partial<AppConfig> = {}): AppConfig {
  return {
    keycloak: { url: "http://localhost:8180", realm: "nebari", clientId: "spa" },
    ...overrides,
  };
}

afterEach(() => {
  // Reset the document mutations applied by applyAppConfig.
  document.title = "LLM API Key Manager";
  for (const el of document.querySelectorAll("style[data-branding]")) {
    el.remove();
  }
  for (const el of document.querySelectorAll("link[rel~='icon']")) {
    el.remove();
  }
});

describe("safeCssValue", () => {
  it("accepts valid CSS token values", () => {
    expect(safeCssValue("#0066cc")).toBe("#0066cc");
    expect(safeCssValue("oklch(55% 0.19 250)")).toBe("oklch(55% 0.19 250)");
    expect(safeCssValue("0.625rem")).toBe("0.625rem");
    expect(safeCssValue("rgb(1 2 3)")).toBe("rgb(1 2 3)");
  });

  it("rejects empty / missing values", () => {
    expect(safeCssValue("")).toBeUndefined();
    expect(safeCssValue(undefined)).toBeUndefined();
  });

  it.each([
    ["rule terminator", "#fff; color: red"],
    ["opening brace", "#fff } body {"],
    ["closing brace", "red}"],
    ["angle brackets", "<script>"],
    ["double quote", 'red"'],
    ["single quote", "red'"],
    ["backslash", "red\\"],
    ["url()", "url(http://evil)"],
    ["url() with space", "url ( x )"],
    ["expression()", "expression(alert(1))"],
    ["javascript:", "javascript:alert(1)"],
  ])("rejects %s", (_label, value) => {
    expect(safeCssValue(value)).toBeUndefined();
  });
});

describe("applyAppConfig", () => {
  it("sets the document title when configured", () => {
    applyAppConfig(baseConfig({ title: "Acme API Keys" }));
    expect(document.title).toBe("Acme API Keys");
  });

  it("leaves the title untouched when not configured", () => {
    document.title = "LLM API Key Manager";
    applyAppConfig(baseConfig());
    expect(document.title).toBe("LLM API Key Manager");
  });

  it("sets a favicon link when configured", () => {
    applyAppConfig(baseConfig({ faviconUrl: "/brand-favicon.svg" }));
    const link = document.querySelector("link[rel~='icon']") as HTMLLinkElement | null;
    expect(link).not.toBeNull();
    expect(link?.getAttribute("href")).toBe("/brand-favicon.svg");
  });

  it("injects theme tokens as kebab-case CSS vars scoped to :root and .dark", () => {
    applyAppConfig(
      baseConfig({
        theme: {
          light: { primary: "#0066cc", primaryForeground: "#ffffff" },
          dark: { primary: "#4da6ff" },
        },
      }),
    );
    const style = document.querySelector("style[data-branding]");
    expect(style).not.toBeNull();
    const css = style?.textContent ?? "";
    expect(css).toContain(":root {");
    expect(css).toContain("--primary: #0066cc;");
    expect(css).toContain("--primary-foreground: #ffffff;");
    expect(css).toContain(".dark {");
    expect(css).toContain("--primary: #4da6ff;");
  });

  it("drops unsafe theme token values while keeping safe ones", () => {
    applyAppConfig(
      baseConfig({
        theme: {
          light: { primary: "#0066cc", background: "red; } body { color: red" },
        },
      }),
    );
    const css = document.querySelector("style[data-branding]")?.textContent ?? "";
    expect(css).toContain("--primary: #0066cc;");
    expect(css).not.toContain("--background");
  });

  it("does not inject a style element when no theme is configured", () => {
    applyAppConfig(baseConfig());
    expect(document.querySelector("style[data-branding]")).toBeNull();
  });

  it("does not inject a style element when theme maps are empty", () => {
    applyAppConfig(baseConfig({ theme: { light: {}, dark: {} } }));
    expect(document.querySelector("style[data-branding]")).toBeNull();
  });
});

describe("loadAppConfig URL sanitisation", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  async function loadWith(config: unknown) {
    vi.resetModules();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => config })) as unknown as typeof fetch,
    );
    const mod = await import("./config");
    return mod.loadAppConfig();
  }

  it("keeps http(s) and root-relative logo/favicon URLs", async () => {
    const cfg = await loadWith({
      keycloak: { url: "u", realm: "r", clientId: "c" },
      logoUrl: "https://cdn.example.com/logo.svg",
      logoUrlDark: "/nebari-logo_dark.svg",
      faviconUrl: "http://example.com/favicon.ico",
    });
    expect(cfg.logoUrl).toBe("https://cdn.example.com/logo.svg");
    expect(cfg.logoUrlDark).toBe("/nebari-logo_dark.svg");
    expect(cfg.faviconUrl).toBe("http://example.com/favicon.ico");
  });

  it("drops javascript:, data:text/html, non-base64 data:image, and malformed URLs", async () => {
    const cfg = await loadWith({
      keycloak: { url: "u", realm: "r", clientId: "c" },
      logoUrl: "javascript:alert(1)",
      logoUrlDark: "not a url",
      faviconUrl: "data:image/svg+xml,<svg/>",
    });
    expect(cfg.logoUrl).toBeUndefined();
    expect(cfg.logoUrlDark).toBeUndefined();
    expect(cfg.faviconUrl).toBeUndefined();

    const cfg2 = await loadWith({
      keycloak: { url: "u", realm: "r", clientId: "c" },
      logoUrl: "data:text/html;base64,PGgxPmhpPC9oMT4=",
      logoUrlDark: "data:application/octet-stream;base64,AAAA",
    });
    expect(cfg2.logoUrl).toBeUndefined();
    expect(cfg2.logoUrlDark).toBeUndefined();
  });

  it("keeps base64-encoded data: image URIs from the allow-list", async () => {
    const cfg = await loadWith({
      keycloak: { url: "u", realm: "r", clientId: "c" },
      logoUrl: "data:image/png;base64,iVBORw0KGgo=",
      faviconUrl: "data:image/svg+xml;base64,PHN2Zy8+",
    });
    expect(cfg.logoUrl).toBe("data:image/png;base64,iVBORw0KGgo=");
    expect(cfg.faviconUrl).toBe("data:image/svg+xml;base64,PHN2Zy8+");
  });
});
