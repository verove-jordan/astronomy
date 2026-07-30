export function humanizeMs(ms: number): string {
  if (!ms) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${Number(s.toFixed(1))}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(Math.floor(s % 60)).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

export function baseName(path: string): string {
  return path.split("/").pop() || path;
}

export function tempC(milliC: number): string {
  return `${Math.round(milliC / 1000)}°C`;
}

// formatBytes renders a byte count as a compact human size (B/KB/MB/GB/TB), e.g. 2254857830 → "2.1 GB".
export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 || v >= 100 ? Math.round(v) : Number(v.toFixed(1))} ${units[i]}`;
}

// formatDurationClock renders an elapsed/remaining span as a clock (H:MM:SS, or M:SS under an hour), e.g.
// 93_000 → "1:33", 3_723_000 → "1:02:03". Used for a transfer's elapsed time + ETA. "—" for a non-positive
// or non-finite span (e.g. an ETA computed while the rate is still 0).
export function formatDurationClock(ms: number): string {
  if (!ms || ms < 0 || !Number.isFinite(ms)) return "—";
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const p = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${p(m)}:${p(s)}` : `${m}:${p(s)}`;
}

// formatRate renders a transfer throughput (bytes per second) as "<size>/s", e.g. 18_874_368 → "18 MB/s".
export function formatRate(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`;
}

// formatTimestamp renders an epoch-ms instant as local "YYYY-MM-DD HH:MM:SS".
export function formatTimestamp(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
    `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  );
}
