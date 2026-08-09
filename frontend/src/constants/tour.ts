// The page-tour registry: which pages have a guided tour, and the ordered steps of each.
//
// Keyed on the ROUTE NAME (router/index.ts) because every real route has one and none carries a
// `meta` block — a name-keyed map is the pattern ProcessingView already uses for its tabs.
//
// A step is just a key. Its copy lives in i18n under `tour.<page>.steps.<key>.{title,body}` in EVERY
// locale (tour.spec.ts fails the build if a locale is missing one), and its screenshot is derived
// from the same pair, so there is exactly one place to add a step and nothing to keep in sync.

export const TOURS: Record<string, readonly string[]> = {
  // Planner
  tonight: ["targets", "score", "skymap", "weather", "darksky", "optics"],
  goto: ["why", "catalogue", "sequence", "chart"],
  calendar: ["months", "kinds", "event"],

  // Capture
  capture: [
    "devices",
    "liveview",
    "camera",
    "filters",
    "sequencer",
    "focus",
    "polar",
  ],
  logbook: ["list", "score", "filters"],
  logbookSession: ["shot", "order", "conditions"],
  mosaic: ["plan", "panels", "progress"],

  // Processing hub
  import: [
    "browse",
    "inspect",
    "mapping",
    "preset",
    "mode",
    "options",
    "reuse",
    "launch",
  ],
  livestack: ["source", "start", "preview", "finalize"],
  jobs: ["list", "status", "open"],
  job: ["progress", "controls", "previews", "results", "stages"],
  runs: ["gallery", "open"],
  library: ["masters", "keys", "mirror"],
  storage: ["connections", "browse", "free", "backup"],

  // Agent
  astroagent: ["chat", "tools", "confirm"],
};

// hasTour reports whether a route name has a tour, so the help button can render nothing rather
// than opening an empty modal.
export function hasTour(page: string | undefined | null): boolean {
  return !!page && (TOURS[page]?.length ?? 0) > 0;
}

// tourSteps returns a page's ordered step keys ([] when it has no tour).
export function tourSteps(page: string): readonly string[] {
  return TOURS[page] ?? [];
}

// tourShot is the screenshot for one step. Shots are generated per locale by `just tour-shots` (see
// tools/demo) into frontend/public/tour/<locale>/, with the focus highlight baked into the image —
// so the modal never has to know where on the page the relevant control is.
export function tourShot(locale: string, page: string, step: string): string {
  return `/tour/${locale}/${page}-${step}.webp`;
}

// TOUR_FALLBACK_LOCALE is served when a page has not been re-shot in the viewer's language. The copy
// beside it is still translated; only the picture is in English.
export const TOUR_FALLBACK_LOCALE = "en";
