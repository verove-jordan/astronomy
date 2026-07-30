<script setup lang="ts">
// A collapsible reference for the advanced-params JSON: every knob the SELECTED MODE exposes, grouped by
// its re-entry tier (finish / prep / re-stack), with a one-line description of what it does. The knobs
// currently set in the JSON above are HIGHLIGHTED and show their live value; the rest are shown muted so
// the user can discover every knob they may add for this mode. When `interactive`, clicking a row cycles
// that knob in the JSON — add with its default → set its opposite value (bool flip / far end of the
// numeric range) → remove — via the `toggle` event, and ↑/↓ on an active numeric row emit `nudge` to
// step the value within min/max. The parent owns the JSON text and the per-mode defaults; this component
// only signals intent (read-only otherwise, so it stays a plain legend anywhere `interactive` is not
// passed).
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { groupsForMode } from "@/constants/paramDocs";
import { oppositeOf } from "@/utils/params";
import type { KnobRange } from "@/stores/jobs";

const props = defineProps<{
  mode: string;
  params?: Record<string, unknown> | null;
  defaults?: Record<string, unknown> | null; // per-mode default values (pipeline.ParamsFor)
  ranges?: Record<string, KnobRange> | null; // per-knob min/max clamp bounds (pipeline.KnobRangesFor)
  interactive?: boolean; // when true, rows are click/keyboard toggles that emit `toggle`
  disabled?: boolean; // when true (e.g. the JSON is invalid), toggling is suppressed
}>();
const emit = defineEmits<{
  (e: "toggle", key: string): void;
  (e: "nudge", key: string, dir: 1 | -1): void;
}>();
const { t } = useI18n();

// A knob is "active" when the current (valid) JSON object carries that key.
function isActive(key: string): boolean {
  return (
    !!props.params && Object.prototype.hasOwnProperty.call(props.params, key)
  );
}

// fmtVal formats a knob value for display (booleans as words, empty string as "", numbers as text).
function fmtVal(v: unknown): string {
  if (v === undefined || v === null) return "";
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "string") return v === "" ? '""' : v;
  return String(v);
}

// The current value for a key, formatted for display (empty when absent / JSON invalid).
function valueOf(key: string): string {
  return fmtVal(props.params?.[key]);
}

// The mode's default value for a key (what toggling the row inserts), formatted for display.
function defaultOf(key: string): string {
  return fmtVal(props.defaults?.[key]);
}

// A key's min/max clamp bounds — present only for numeric knobs (booleans/enums carry none).
function rangeOf(key: string): KnobRange | undefined {
  return props.ranges?.[key] ?? undefined;
}

// fmtBound trims float noise off a min/max bound (integer knobs render without decimals).
function fmtBound(n: number, isInt?: boolean): string {
  if (!Number.isFinite(n)) return "";
  return isInt ? String(Math.round(n)) : String(parseFloat(n.toFixed(4)));
}

// hasMeta reports whether there is any default/range to show — keeps the component a plain legend
// when it is used without the defaults/ranges props.
function hasMeta(key: string): boolean {
  return defaultOf(key) !== "" || rangeOf(key) !== undefined;
}

// Ask the parent to cycle the knob (add default / flip to opposite / remove) — only when enabled.
function onToggle(key: string): void {
  if (!props.interactive || props.disabled) return;
  emit("toggle", key);
}

// canFlip: the next click sets the knob's "opposite" value (bool flip / far range end) rather than
// removing it — false for enums (no opposite) and once the value already IS the opposite.
function canFlip(key: string): boolean {
  if (!isActive(key)) return false;
  const opp = oppositeOf(props.defaults?.[key], rangeOf(key));
  return opp !== undefined && props.params?.[key] !== opp;
}

// rowTitle is the 3-state tooltip: add with default → set the opposite → remove; active numeric rows
// also mention the ↑/↓ stepping.
function rowTitle(key: string): string | undefined {
  if (!props.interactive) return undefined;
  let title: string;
  if (!isActive(key)) title = t("paramDocs.add");
  else if (canFlip(key)) title = t("paramDocs.flip");
  else title = t("paramDocs.remove");
  if (isActive(key) && rangeOf(key)) title += " · " + t("paramDocs.arrows");
  return title;
}

// onNudge steps an ACTIVE NUMERIC knob with ↑/↓; other rows leave the keys to the page (no
// preventDefault, so normal scrolling still works).
function onNudge(ev: KeyboardEvent, key: string, dir: 1 | -1): void {
  if (!props.interactive || props.disabled) return;
  if (!isActive(key) || !rangeOf(key)) return;
  ev.preventDefault();
  emit("nudge", key, dir);
}

const groups = computed(() => groupsForMode(props.mode));
</script>

<template>
  <details
    class="mt-3 rounded-md border border-slate-200 dark:border-slate-700"
  >
    <summary
      class="cursor-pointer px-3 py-2 text-xs font-medium text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
    >
      {{ t("paramDocs.title") }}
    </summary>
    <div class="space-y-4 px-3 pb-3">
      <p class="text-xs text-slate-400">{{ t("paramDocs.activeLegend") }}</p>
      <section v-for="g in groups" :key="g.titleKey">
        <h4
          class="flex flex-wrap items-baseline gap-x-2 text-xs font-semibold text-slate-600 dark:text-slate-300"
        >
          {{ t(g.titleKey) }}
          <span class="font-normal text-slate-400">— {{ t(g.hintKey) }}</span>
        </h4>
        <dl class="mt-1 space-y-1">
          <div
            v-for="k in g.keys"
            :key="k"
            class="grid grid-cols-1 gap-x-3 rounded px-1.5 py-1 sm:grid-cols-[minmax(9rem,auto)_1fr]"
            :class="[
              isActive(k)
                ? 'bg-brand-50 ring-1 ring-brand-200 dark:bg-brand-500/10 dark:ring-brand-500/30'
                : '',
              interactive && !disabled
                ? isActive(k)
                  ? 'cursor-pointer select-none hover:ring-brand-300 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-400 dark:hover:ring-brand-500/50'
                  : 'cursor-pointer select-none hover:bg-slate-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-400 dark:hover:bg-slate-800/60'
                : '',
              interactive && disabled ? 'cursor-not-allowed' : '',
            ]"
            :role="interactive ? 'button' : undefined"
            :tabindex="interactive && !disabled ? 0 : undefined"
            :aria-pressed="interactive ? isActive(k) : undefined"
            :aria-disabled="interactive && disabled ? 'true' : undefined"
            :title="rowTitle(k)"
            @click="onToggle(k)"
            @keydown.enter.prevent="onToggle(k)"
            @keydown.space.prevent="onToggle(k)"
            @keydown.up="onNudge($event, k, 1)"
            @keydown.down="onNudge($event, k, -1)"
          >
            <dt
              class="flex items-center gap-2 font-mono text-xs"
              :class="
                isActive(k)
                  ? 'font-semibold text-brand-700 dark:text-brand-300'
                  : 'text-slate-500 dark:text-slate-400'
              "
            >
              <span
                v-if="interactive"
                aria-hidden="true"
                class="inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-sm text-[11px] font-semibold leading-none"
                :class="
                  isActive(k)
                    ? 'bg-brand-100 text-brand-700 dark:bg-brand-500/20 dark:text-brand-200'
                    : 'bg-slate-100 text-slate-500 dark:bg-slate-700 dark:text-slate-300'
                "
                >{{ isActive(k) ? "−" : "+" }}</span
              >
              <span>{{ k }}</span>
              <span
                v-if="isActive(k)"
                class="font-sans font-semibold text-brand-500 dark:text-brand-300"
                >{{ valueOf(k) || t("paramDocs.set") }}</span
              >
            </dt>
            <dd
              class="text-xs"
              :class="
                isActive(k)
                  ? 'text-slate-600 dark:text-slate-300'
                  : 'text-slate-500 dark:text-slate-400'
              "
            >
              <span class="block">{{ t(`paramDocs.${k}`) }}</span>
              <!-- Default / min / max reference: bold, colour-coded so the usable range reads at a glance. -->
              <span
                v-if="hasMeta(k)"
                class="mt-0.5 flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 font-mono text-[11px] leading-tight"
              >
                <span
                  v-if="defaultOf(k) !== ''"
                  class="inline-flex items-baseline gap-1"
                >
                  <span class="text-slate-400 dark:text-slate-500">{{
                    t("paramDocs.default")
                  }}</span>
                  <span
                    class="font-semibold text-success-700 dark:text-success-300"
                    >{{ defaultOf(k) }}</span
                  >
                </span>
                <template v-if="rangeOf(k)">
                  <span class="inline-flex items-baseline gap-1">
                    <span class="text-slate-400 dark:text-slate-500">{{
                      t("paramDocs.min")
                    }}</span>
                    <span
                      class="font-semibold text-info-700 dark:text-info-300"
                      >{{ fmtBound(rangeOf(k)!.min, rangeOf(k)!.int) }}</span
                    >
                  </span>
                  <span class="inline-flex items-baseline gap-1">
                    <span class="text-slate-400 dark:text-slate-500">{{
                      t("paramDocs.max")
                    }}</span>
                    <span
                      class="font-semibold text-warning-700 dark:text-warning-300"
                      >{{ fmtBound(rangeOf(k)!.max, rangeOf(k)!.int) }}</span
                    >
                  </span>
                </template>
              </span>
            </dd>
          </div>
        </dl>
      </section>
    </div>
  </details>
</template>
