// Centralized fetch wrapper. Stores call these; components never fetch directly.

export const BASE = import.meta.env.VITE_API_BASE || "http://localhost:8080";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
    signal,
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const data = (await res.json()) as { error?: string };
      if (data.error) message = data.error;
    } catch {
      // keep statusText
    }
    throw new ApiError(res.status, message);
  }
  return (await res.json()) as T;
}

export const apiGet = <T>(path: string, signal?: AbortSignal) =>
  request<T>("GET", path, undefined, signal);
export const apiPost = <T>(path: string, body?: unknown) =>
  request<T>("POST", path, body);

export const fileUrl = (path: string) =>
  `${BASE}/api/file?path=${encodeURIComponent(path)}`;
export const eventsUrl = (jobId: number) => `${BASE}/api/jobs/${jobId}/events`;
export const previewUrl = (path: string, max?: number) =>
  `${BASE}/api/preview?path=${encodeURIComponent(path)}${
    max ? `&max=${max}` : ""
  }`;
