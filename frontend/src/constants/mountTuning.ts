// Mount-tuning knowledge registry for the /goto helper: known mechanical issues per mount model and
// the compensations that fix them. This file carries STRUCTURE only (ids + severity/effort metadata);
// every piece of copy lives in i18n under `goto.tuning.models.<model>.*` so both locales stay complete.
// The backend has no mount-model concept (its registry is alignment profiles) — this is frontend-only.

export type IssueSeverity = "high" | "medium";
export type FixEffort = "free" | "paid";

export interface MountIssue {
  id: string;
  severity: IssueSeverity;
}

export interface MountFix {
  id: string;
  effort: FixEffort;
  minutes?: number; // rough hands-on time
}

export interface MountTuning {
  profiles: string[]; // alignment-profile keys that default to this model
  issues: MountIssue[];
  fixes: MountFix[];
}

// Keys double as i18n path segments; insertion order is the <select> order.
export const MOUNT_TUNING: Record<string, MountTuning> = {
  avx: {
    profiles: ["celestron-eq"],
    issues: [
      { id: "dec_backlash", severity: "high" },
      { id: "periodic_error", severity: "high" },
    ],
    fixes: [
      { id: "east_heavy", effort: "free", minutes: 10 },
      { id: "dec_unidirectional", effort: "free", minutes: 15 },
      { id: "guiding_assistant", effort: "free", minutes: 15 },
      { id: "pec", effort: "free", minutes: 30 },
    ],
  },
  "celestron-eq-generic": {
    profiles: [],
    issues: [
      { id: "backlash", severity: "medium" },
      { id: "periodic_error", severity: "medium" },
    ],
    fixes: [
      { id: "balance", effort: "free", minutes: 10 },
      { id: "cables", effort: "free", minutes: 5 },
      { id: "polar_quality", effort: "free", minutes: 20 },
      { id: "backlash_measure", effort: "free", minutes: 15 },
    ],
  },
  "synscan-eq-generic": {
    profiles: ["synscan-eq"],
    issues: [
      { id: "backlash", severity: "medium" },
      { id: "periodic_error", severity: "medium" },
    ],
    fixes: [
      { id: "balance", effort: "free", minutes: 10 },
      { id: "cables", effort: "free", minutes: 5 },
      { id: "polar_quality", effort: "free", minutes: 20 },
      { id: "backlash_measure", effort: "free", minutes: 15 },
    ],
  },
  "altaz-generic": {
    profiles: ["altaz-generic", "synscan-altaz", "celestron-altaz"],
    issues: [
      { id: "field_rotation", severity: "high" },
      { id: "backlash", severity: "medium" },
    ],
    fixes: [
      { id: "short_exposures", effort: "free" },
      { id: "careful_align", effort: "free", minutes: 10 },
      { id: "balance", effort: "free", minutes: 5 },
    ],
  },
  generic: {
    profiles: ["eq-generic"],
    issues: [
      { id: "backlash", severity: "medium" },
      { id: "periodic_error", severity: "medium" },
    ],
    fixes: [
      { id: "balance", effort: "free", minutes: 10 },
      { id: "cables", effort: "free", minutes: 5 },
      { id: "polar_quality", effort: "free", minutes: 20 },
      { id: "backlash_measure", effort: "free", minutes: 15 },
    ],
  },
};

export const MOUNT_MODELS = Object.keys(MOUNT_TUNING);

// defaultModelForProfile maps the selected alignment profile to the most likely mount model (used
// until the user explicitly picks one).
export function defaultModelForProfile(profile: string): string {
  for (const [model, entry] of Object.entries(MOUNT_TUNING)) {
    if (entry.profiles.includes(profile)) return model;
  }
  return "generic";
}
