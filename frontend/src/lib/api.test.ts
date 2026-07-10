import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiError, api } from "./api";

function mockFetch(response: Partial<Response> & { jsonBody?: unknown; textBody?: string }) {
  const resp = {
    ok: response.ok ?? true,
    status: response.status ?? 200,
    json: async () => response.jsonBody,
    text: async () => response.textBody ?? "",
  } as unknown as Response;
  return vi.fn().mockResolvedValue(resp);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("api", () => {
  it("parses a JSON body on success", async () => {
    vi.stubGlobal("fetch", mockFetch({ jsonBody: { hello: "world" } }));
    await expect(api.get<{ hello: string }>("/api/x")).resolves.toEqual({ hello: "world" });
  });

  it("returns null for 204 responses", async () => {
    vi.stubGlobal("fetch", mockFetch({ status: 204 }));
    await expect(api.delete("/api/x")).resolves.toBeNull();
  });

  it("throws ApiError with status on a non-2xx response", async () => {
    vi.stubGlobal("fetch", mockFetch({ ok: false, status: 403, textBody: "forbidden" }));
    await expect(api.post("/api/x", {})).rejects.toMatchObject({
      name: "ApiError",
      status: 403,
    });
  });

  it("serializes the request body as JSON and attaches a bearer token", async () => {
    const fetchMock = mockFetch({ jsonBody: {} });
    vi.stubGlobal("fetch", fetchMock);
    await api.post("/api/x", { a: 1 });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/x",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ a: 1 }),
        headers: expect.objectContaining({ Authorization: "Bearer test-token" }),
      }),
    );
  });

  it("retries once on a 401", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({}),
        text: async () => "",
      } as unknown as Response)
      .mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ ok: true }),
        text: async () => "",
      } as unknown as Response);
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/x")).resolves.toEqual({ ok: true });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("exposes ApiError as an Error subclass", () => {
    expect(new ApiError(500, "boom")).toBeInstanceOf(Error);
  });
});
