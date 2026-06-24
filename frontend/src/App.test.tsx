import { afterEach, describe, expect, it, vi } from "vitest";

import { renderWithProviders } from "@/test/render";

import App from "./App";

function jsonResponse(body: unknown) {
  return { ok: true, status: 200, json: async () => body, text: async () => "" } as Response;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("App", () => {
  it("renders the page shell", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (input.startsWith("/api/me")) {
          return Promise.resolve(
            jsonResponse({ username: "jane", name: "Jane Doe", email: "jane@x.io", groups: [] }),
          );
        }
        if (input.startsWith("/api/models")) {
          return Promise.resolve(jsonResponse({ models: [] }));
        }
        return Promise.resolve(jsonResponse({ keys: [] }));
      }),
    );

    const { findByRole, getByText } = renderWithProviders(<App />);
    expect(getByText("LLM API Key Manager", { selector: "h1" })).toBeInTheDocument();
    expect(await findByRole("button", { name: /create new key/i })).toBeInTheDocument();
  });
});
