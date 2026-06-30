// Observer-site timezone helpers. The backend returns every instant as a UTC epoch-ms; the UI formats
// those in the SELECTED LOCATION's timezone (derived from its coordinates), so "tonight" reads in the
// scope site's wall-clock — not the browser's. tz-lookup resolves an IANA name from lat/lon offline.
import tzlookup from "tz-lookup";

function browserTz(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

// tzForLocation returns the IANA timezone for a coordinate, falling back to the browser tz.
export function tzForLocation(lat: number, lon: number): string {
  try {
    return tzlookup(lat, lon);
  } catch {
    return browserTz();
  }
}

// fmtClock formats a UTC ms as 24-hour HH:mm in the given timezone.
export function fmtClock(ms: number, tz: string): string {
  return new Intl.DateTimeFormat("en-GB", {
    timeZone: tz,
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(new Date(ms));
}

// fmtDateTime formats a UTC ms as "YYYY-MM-DD HH:mm" in the given timezone.
export function fmtDateTime(ms: number, tz: string): string {
  return new Intl.DateTimeFormat("sv-SE", {
    timeZone: tz,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).format(new Date(ms));
}

// offsetMinutes is the UTC offset (minutes) of tz at a given instant.
function offsetMinutes(tz: string, date: Date): number {
  const parts: Record<string, string> = {};
  for (const p of new Intl.DateTimeFormat("en-US", {
    timeZone: tz,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(date)) {
    parts[p.type] = p.value;
  }
  const asUTC = Date.UTC(
    +parts.year,
    +parts.month - 1,
    +parts.day,
    +parts.hour,
    +parts.minute,
    +parts.second,
  );
  return (asUTC - date.getTime()) / 60000;
}

// zonedWallToISO converts a "YYYY-MM-DDTHH:mm" wall-clock (interpreted in tz) to a UTC ISO string —
// for sending a user-picked date/time (in the observer's local time) to the backend as `at`.
export function zonedWallToISO(local: string, tz: string): string {
  const [datePart, timePart] = local.split("T");
  const [y, mo, d] = datePart.split("-").map(Number);
  const [h, mi] = timePart.split(":").map(Number);
  const guessUTC = Date.UTC(y, mo - 1, d, h, mi);
  const off = offsetMinutes(tz, new Date(guessUTC));
  return new Date(guessUTC - off * 60000).toISOString();
}

// nowInZone renders a UTC ms as a "YYYY-MM-DDTHH:mm" wall-clock string in tz (for a datetime-local input).
export function nowInZone(ms: number, tz: string): string {
  const parts: Record<string, string> = {};
  for (const p of new Intl.DateTimeFormat("en-CA", {
    timeZone: tz,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).formatToParts(new Date(ms))) {
    parts[p.type] = p.value;
  }
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}
