import { describe, it, expect } from "vitest";
import { TOURS, hasTour, tourSteps, tourShot } from "./tour";
import router from "@/router";
import en from "@/i18n/en.json";
import fr from "@/i18n/fr.json";

type Messages = Record<string, unknown>;
const locales: Record<string, Messages> = {
  en: en as Messages,
  fr: fr as Messages,
};

function at(messages: Messages, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (o, k) => (o && typeof o === "object" ? (o as Messages)[k] : undefined),
      messages,
    );
}

describe("tour registry", () => {
  it("only names routes that exist", () => {
    // A tour keyed on a route name that has been renamed would silently never open, because the
    // help button reads route.name and would find no entry.
    const names = new Set(
      router
        .getRoutes()
        .filter((r) => r.name)
        .map((r) => String(r.name)),
    );
    for (const page of Object.keys(TOURS))
      expect(names.has(page), `route "${page}"`).toBe(true);
  });

  it("covers every page a user can navigate to", () => {
    // The requirement is a tour on EVERY page, so a new named route without one is a gap, not a
    // choice. If a page genuinely should not have a tour, exempt it here with a reason.
    const exempt = new Set<string>();
    const missing = router
      .getRoutes()
      .filter((r) => r.name && r.components) // skip redirect-only records
      .map((r) => String(r.name))
      .filter((n) => !exempt.has(n) && !TOURS[n]);
    expect(missing, "named routes with no tour").toEqual([]);
  });

  it("has unique, non-empty steps per page", () => {
    for (const [page, steps] of Object.entries(TOURS)) {
      expect(steps.length, page).toBeGreaterThan(0);
      expect(new Set(steps).size, `${page} has duplicate steps`).toBe(
        steps.length,
      );
      for (const s of steps) expect(s, `${page} step`).toBeTruthy();
    }
  });

  it("reports availability honestly", () => {
    expect(hasTour("tonight")).toBe(true);
    expect(hasTour("no-such-page")).toBe(false);
    expect(hasTour("")).toBe(false);
    expect(hasTour(undefined)).toBe(false);
    expect(tourSteps("no-such-page")).toEqual([]);
  });

  it("derives a distinct shot path per step", () => {
    const seen = new Set<string>();
    for (const [page, steps] of Object.entries(TOURS)) {
      for (const s of steps) {
        const path = tourShot("en", page, s);
        expect(seen.has(path), `duplicate shot ${path}`).toBe(false);
        seen.add(path);
        expect(path).toBe(`/tour/en/${page}-${s}.webp`);
      }
    }
  });
});

// The tour is copy-heavy and split across two locales, which is exactly the shape that rots. This
// mirrors the StackingPanel locale-parity test: every page and every step must carry a real title
// and body in EVERY locale, or the modal renders the raw key at the user.
describe("tour copy", () => {
  for (const [locale, messages] of Object.entries(locales)) {
    it(`${locale}: has the shared chrome strings`, () => {
      for (const key of [
        "tour.open",
        "tour.help",
        "tour.done",
        "tour.title",
        "tour.stepOf",
        "tour.noShot",
      ])
        expect(at(messages, key), `${locale}: ${key}`).toBeTruthy();
    });

    for (const [page, steps] of Object.entries(TOURS)) {
      it(`${locale}: ${page} is fully translated`, () => {
        expect(
          at(messages, `tour.${page}.title`),
          `tour.${page}.title`,
        ).toBeTruthy();
        for (const s of steps) {
          const base = `tour.${page}.steps.${s}`;
          const title = at(messages, `${base}.title`);
          const body = at(messages, `${base}.body`);
          expect(title, `${base}.title`).toBeTruthy();
          expect(body, `${base}.body`).toBeTruthy();
          // Guard against a placeholder slipping in: a body shorter than its own title is not copy.
          expect(
            String(body).length,
            `${base}.body is too short`,
          ).toBeGreaterThan(String(title).length);
        }
      });
    }
  }

  it("defines no copy for a page that is not in the registry", () => {
    const tour = (en as Messages).tour as Messages;
    const chrome = new Set([
      "open",
      "help",
      "done",
      "title",
      "stepOf",
      "noShot",
    ]);
    for (const key of Object.keys(tour)) {
      if (chrome.has(key)) continue;
      expect(TOURS[key], `copy for unregistered page "${key}"`).toBeDefined();
    }
  });
});
