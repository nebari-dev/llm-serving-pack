import { describe, expect, it } from "vitest";

import css from "./index.css?raw";

// Branding from /config.json can only override the tokens listed in
// ThemeTokens (app/config.ts). Any token in index.css that is a plain shade of
// one of those must therefore reference it with var() instead of repeating the
// Nebari literal, or a rebranded deployment keeps flashing Nebari magenta on
// hover. Re-pulling the theme (`shadcn add @nebari/theme`) reintroduces the
// literals, so this guards the derivation.

function block(selector: string): string {
  const match = new RegExp(`${selector}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) {
    throw new Error(`no ${selector} block found in index.css`);
  }
  return match[1];
}

describe.each([
  ["light", block(":root")],
  ["dark", block("\\.dark")],
])("%s theme tokens", (_mode, declarations) => {
  it.each([
    ["--sidebar-primary", "var(--primary)"],
    ["--sidebar-primary-foreground", "var(--primary-foreground)"],
    ["--sidebar-ring", "var(--ring)"],
  ])("derives %s from %s", (token, source) => {
    expect(declarations).toContain(`${token}: ${source};`);
  });

  it("derives --primary-hover from --primary", () => {
    const declaration = /--primary-hover:([^;]*);/.exec(declarations)?.[1];
    expect(declaration).toBeDefined();
    expect(declaration).toContain("var(--primary)");
  });
});
