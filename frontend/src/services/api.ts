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
export const apiPut = <T>(path: string, body?: unknown) =>
  request<T>("PUT", path, body);
export const apiDelete = <T>(path: string) => request<T>("DELETE", path);

// Non-secret S3 UI selection, persisted by the S3 store. Owned here (the lowest-level module) so the
// file/preview/thumb URL builders and the list endpoints can tag every request with the active
// bucket/prefix — giving transparent S3 fallback for previews and results everywhere — without importing
// the store (which would cycle). Credentials never appear here; they live only in the backend env.
export const S3_BUCKET_KEY = "astrostack.s3.bucket";
export const S3_PREFIX_KEY = "astrostack.s3.prefix";

// s3Suffix returns "&bucket=…&prefix=…" for the active S3 selection (empty when none), for URLs that
// already carry a "?path=". The backend serves local-first and only falls back to the S3 mirror when the
// local file was freed, so tagging every URL is harmless (and free) when the file is still on disk.
export function s3Suffix(): string {
  try {
    const bucket = localStorage.getItem(S3_BUCKET_KEY) || "";
    if (!bucket) return "";
    const prefix = localStorage.getItem(S3_PREFIX_KEY) || "";
    return `&bucket=${encodeURIComponent(bucket)}${
      prefix ? `&prefix=${encodeURIComponent(prefix)}` : ""
    }`;
  } catch {
    return "";
  }
}

// withS3 appends the active bucket/prefix to a request path, choosing "?" or "&" — for list endpoints
// (runs, processed) that must fall back to the S3 mirror when the local tree was freed.
export function withS3(path: string): string {
  const suffix = s3Suffix();
  if (!suffix) return path;
  return path + (path.includes("?") ? suffix : "?" + suffix.slice(1));
}

export const fileUrl = (path: string) =>
  `${BASE}/api/file?path=${encodeURIComponent(path)}${s3Suffix()}`;
// thumbUrl is a small server-resized JPEG of an output image — used by the Runs gallery instead of the
// full-resolution PNG so the page loads fast (the full image is fetched only when a run is opened).
export const thumbUrl = (path: string, w?: number) =>
  `${BASE}/api/thumb?path=${encodeURIComponent(path)}${w ? `&w=${w}` : ""}${s3Suffix()}`;
export const eventsUrl = (jobId: number) => `${BASE}/api/jobs/${jobId}/events`;
export const agentTurnEventsUrl = (turnId: string) =>
  `${BASE}/api/agent/turns/${turnId}/events`;
export const previewUrl = (path: string, max?: number) =>
  `${BASE}/api/preview?path=${encodeURIComponent(path)}${
    max ? `&max=${max}` : ""
  }${s3Suffix()}`;
