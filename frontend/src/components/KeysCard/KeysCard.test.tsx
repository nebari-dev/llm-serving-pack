import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { renderWithProviders } from "@/test/render";

import { KeysCard } from "./KeysCard";

function stubKeys(body: unknown, ok = true, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok,
      status,
      json: async () => body,
      text: async () => "boom",
    } as unknown as Response),
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("KeysCard", () => {
  it("shows the empty state when there are no keys", async () => {
    stubKeys({ keys: [] });
    renderWithProviders(<KeysCard />);
    expect(await screen.findByText("No API keys yet.")).toBeInTheDocument();
  });

  it("renders a row per key", async () => {
    stubKeys({
      keys: [
        {
          clientId: "user-jane-1",
          creator: "jane",
          description: "Research notebook",
          createdAt: "2026-01-02T00:00:00Z",
          modelName: "llama-3",
          namespace: "ns",
        },
      ],
    });
    renderWithProviders(<KeysCard />);
    expect(await screen.findByText("Research notebook")).toBeInTheDocument();
    expect(screen.getByText("user-jane-1")).toBeInTheDocument();
    expect(screen.getByText("llama-3")).toBeInTheDocument();
  });

  it("shows an error state when the request fails", async () => {
    stubKeys({}, false, 500);
    renderWithProviders(<KeysCard />);
    await waitFor(() => expect(screen.getByText(/Failed to load keys/i)).toBeInTheDocument());
  });
});
