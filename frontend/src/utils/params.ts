import type { KnobRange } from "@/stores/jobs";

// Pure helpers for the pipeline-params glossary interactions (ParamGlossary + ImportView): the 3-state
// click cycle (absent → default → opposite → removed) and the ↑/↓ keyboard stepping. A knob's type is
// inferred at runtime — one with a KnobRange is numeric (range.int marks integers), a boolean default is
// a checkbox knob, anything else (enums like palette/look) has no "opposite".

// oppositeOf returns the value the cycle's second click sets: booleans flip their default; numeric knobs
// jump to the far end of [min,max] from the default (so 0→1 and 1→0 on a 0..1 knob); enums/strings
// return undefined (their cycle is just add → remove).
export function oppositeOf(def: unknown, range?: KnobRange): unknown {
  if (typeof def === "boolean") return !def;
  if (range && typeof def === "number" && Number.isFinite(def)) {
    const far = def - range.min <= range.max - def ? range.max : range.min;
    return range.int ? Math.round(far) : far;
  }
  return undefined;
}

// stepFor derives the ↑/↓ increment for a numeric knob: 1 for integers, else ~1/20 of the span snapped
// to one significant digit (≥ 0.01) so steps read as clean values (0.05, 0.1, 0.2, …).
export function stepFor(range: KnobRange): number {
  if (range.int) return 1;
  const span = Math.abs(range.max - range.min);
  if (!Number.isFinite(span) || span <= 0) return 0.1;
  const raw = span / 20;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const snapped = parseFloat((Math.round(raw / mag) * mag).toPrecision(2));
  return Math.max(0.01, snapped);
}

// nudged returns the knob's next value after one ↑/↓ press: the current value (falling back to the
// default, then min, when it doesn't parse) ± one step, clamped to [min,max], with float noise trimmed.
export function nudged(
  current: unknown,
  dir: 1 | -1,
  range: KnobRange,
  def?: unknown,
): number {
  const cur = Number(current);
  const fallback = Number(def);
  const base = Number.isFinite(cur)
    ? cur
    : Number.isFinite(fallback)
      ? fallback
      : range.min;
  const next = Math.min(
    range.max,
    Math.max(range.min, base + dir * stepFor(range)),
  );
  return range.int ? Math.round(next) : parseFloat(next.toFixed(4));
}
