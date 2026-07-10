import { getToken } from "@/auth/keycloak";

/** Error thrown for any non-2xx response, carrying the HTTP status. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  // Auth is SPA-managed Keycloak: attach the current access token as a bearer
  // on every call. getToken() refreshes it first if it is near expiry.
  const exec = async (forceRefresh = false) => {
    const token = await getToken(forceRefresh);
    const opts: RequestInit = {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
    };
    if (body !== undefined) {
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts);
  };

  // The token can expire between getToken() and the server receiving it; on a
  // 401, force a refresh and retry once.
  let resp = await exec();
  if (resp.status === 401) {
    resp = await exec(true);
  }

  if (!resp.ok) {
    const text = await resp.text().catch(() => "");
    throw new ApiError(resp.status, `${method} ${path} failed (${resp.status}): ${text.trim()}`);
  }
  // 204 No Content (e.g. DELETE) has no body to parse.
  if (resp.status === 204) {
    return null as T;
  }
  return (await resp.json()) as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  delete: <T = void>(path: string) => request<T>("DELETE", path),
};
