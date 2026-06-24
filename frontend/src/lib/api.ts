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
  const opts: RequestInit = {
    method,
    headers: { "Content-Type": "application/json" },
  };
  if (body !== undefined) {
    opts.body = JSON.stringify(body);
  }

  const resp = await fetch(path, opts);
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
