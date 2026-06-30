// Compose localized labels for sky-calendar events from the backend's structured fields (kind + bodies
// + magnitudes), so the same event reads correctly in EN and FR. The backend never sends a prebuilt
// sentence (except comet/shower proper names, which need no translation).
import type { SkyEvent } from "@/types";

type T = (key: string, named?: Record<string, unknown>) => string;

// bodyLabel localizes a canonical body key (sun/moon/planets/iss…), falling back to the raw key.
export function bodyLabel(key: string, t: T): string {
  if (!key) return "";
  const k = `calendar.bodies.${key}`;
  const v = t(k);
  return v === k ? key : v;
}

export function kindLabel(e: SkyEvent, t: T): string {
  return t(`calendar.kinds.${e.kind}`);
}

export function instrumentLabel(tier: string, t: T): string {
  return t(`calendar.tiers.${tier}`);
}

// eventTitle builds the human, localized one-line title for an event.
export function eventTitle(e: SkyEvent, t: T): string {
  const b = (i: number) => bodyLabel(e.bodies?.[i] ?? "", t);
  const sep = e.separation_deg != null ? e.separation_deg.toFixed(1) : "";
  switch (e.kind) {
    case "conjunction":
      return t("calendar.titleFmt.conjunction", { a: b(0), b: b(1), sep });
    case "planet_moon":
      return t("calendar.titleFmt.planet_moon", { body: b(0), sep });
    case "opposition":
      return t("calendar.titleFmt.opposition", { body: b(0) });
    case "elongation":
      return t("calendar.titleFmt.elongation", { body: b(0) });
    case "solar_eclipse":
      return t("calendar.titleFmt.solar_eclipse", {
        type: t(`calendar.eclipseType.${e.subtype || "partial"}`),
      });
    case "lunar_eclipse":
      return t("calendar.titleFmt.lunar_eclipse", {
        type: t(`calendar.eclipseType.${e.subtype || "penumbral"}`),
      });
    case "satellite_transit":
      return t("calendar.titleFmt.satellite_transit", {
        sat: b(0),
        body: bodyLabel(e.subtype || "sun", t),
      });
    case "moon_phase":
      return t(`calendar.phase.${e.subtype || "full"}`);
    case "supermoon":
      return t("calendar.kinds.supermoon");
    case "equinox":
    case "solstice":
      return t(`calendar.season.${e.subtype || ""}`);
    case "perihelion":
    case "aphelion":
      return t(`calendar.season.${e.kind}`);
    case "meteor_shower":
    case "comet":
      return e.title; // proper name from the backend — no translation needed
    default:
      return e.title;
  }
}

// eventKindPill maps a kind to a complete (JIT-safe) Tailwind chip class for the table/calendar.
export const eventKindPill: Record<string, string> = {
  solar_eclipse:
    "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
  lunar_eclipse:
    "bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300",
  conjunction:
    "bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300",
  opposition:
    "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
  elongation:
    "bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300",
  planet_moon:
    "bg-indigo-100 text-indigo-800 dark:bg-indigo-900/40 dark:text-indigo-300",
  moon_phase:
    "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
  supermoon:
    "bg-violet-100 text-violet-800 dark:bg-violet-900/40 dark:text-violet-300",
  equinox: "bg-teal-100 text-teal-800 dark:bg-teal-900/40 dark:text-teal-300",
  solstice: "bg-teal-100 text-teal-800 dark:bg-teal-900/40 dark:text-teal-300",
  perihelion:
    "bg-slate-100 text-slate-600 dark:bg-slate-700/50 dark:text-slate-300",
  aphelion:
    "bg-slate-100 text-slate-600 dark:bg-slate-700/50 dark:text-slate-300",
  meteor_shower:
    "bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300",
  comet: "bg-cyan-100 text-cyan-800 dark:bg-cyan-900/40 dark:text-cyan-300",
  satellite_transit:
    "bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-300",
};

export function kindPillClass(kind: string): string {
  return (
    eventKindPill[kind] ??
    "bg-slate-100 text-slate-700 dark:bg-slate-700/40 dark:text-slate-200"
  );
}
