// Storage-class catalogue for the S3 Glacier UI (explorer "change storage class", connection default
// class, backup archival). Structural facts only — the human label + explanation live in i18n
// (storageClass.classes.<id>.{label,blurb}) so they translate. The one rule the UI must encode: an
// ARCHIVED class needs a thaw (restore) before its objects can be read; GLACIER_IR is INSTANT despite its
// name. Mirrors the backend model in internal/s3store/glacier.go.

export type StorageFamily = "instant" | "archived";

export interface StorageClassMeta {
  id: string; // canonical AWS name (also what the API expects)
  family: StorageFamily;
  needsThaw: boolean; // archived → a restore is required before read
  minDurationDays: number; // min-storage-duration → early-delete charge if removed/retiered sooner
  providers: string[]; // where it exists: "aws" | "scaleway"
}

// The classes offered as transition targets, coldest last. STANDARD is the hot "classic" tier.
export const STORAGE_CLASSES: StorageClassMeta[] = [
  {
    id: "STANDARD",
    family: "instant",
    needsThaw: false,
    minDurationDays: 0,
    providers: ["aws", "scaleway"],
  },
  {
    id: "GLACIER_IR",
    family: "instant",
    needsThaw: false,
    minDurationDays: 90,
    providers: ["aws"],
  },
  {
    id: "GLACIER",
    family: "archived",
    needsThaw: true,
    minDurationDays: 90,
    providers: ["aws", "scaleway"],
  },
  {
    id: "DEEP_ARCHIVE",
    family: "archived",
    needsThaw: true,
    minDurationDays: 180,
    providers: ["aws"],
  },
];

// Instant-only subset — the legal choices for a write DEFAULT class (uploads must stay readable).
export const INSTANT_CLASSES = STORAGE_CLASSES.filter(
  (c) => c.family === "instant",
);

// Retrieval tiers for a thaw (speed vs cost). Standard is the app default.
export const RESTORE_TIERS = ["Standard", "Bulk", "Expedited"] as const;
export type RestoreTier = (typeof RESTORE_TIERS)[number];

export function storageClassMeta(id: string): StorageClassMeta | undefined {
  const u = (id || "STANDARD").toUpperCase();
  return STORAGE_CLASSES.find((c) => c.id === u);
}

// isArchivedClass mirrors the backend: only GLACIER (Flexible) and DEEP_ARCHIVE need a thaw. "" == STANDARD.
export function isArchivedClass(id: string): boolean {
  const u = (id || "").toUpperCase();
  return u === "GLACIER" || u === "DEEP_ARCHIVE";
}

// classLabelId normalizes a raw storage class ("" → STANDARD) to a catalogue id for display.
export function classLabelId(id: string): string {
  return (id || "STANDARD").toUpperCase();
}
