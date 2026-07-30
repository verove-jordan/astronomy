<script setup lang="ts">
import { computed, ref, watchEffect } from "vue";
import { useI18n } from "vue-i18n";
import { card, frameTypeAccentClass } from "@/constants/styles";
import { humanizeMs, tempC } from "@/utils/format";
import FilterChip from "@/components/Common/FilterChip.vue";
import type { CalibPreview, CalibChannel, CalibSuggestion } from "@/types";

// readonly (job page): render as a summary of what's included — no checkboxes, only the masters that
// were kept, and a fuller per-master param line. Interactive default (import panel): checkboxes to skip.
const props = defineProps<{
  preview: CalibPreview | null;
  readonly?: boolean;
}>();
const { t } = useI18n();

// Two-way: the suggestion ids the user excluded (unchecked) — threaded into the run as calib_exclude.
const excluded = defineModel<string[]>("excluded", { default: () => [] });

// In readonly mode show only the INCLUDED masters (not excluded); interactive shows every suggestion.
function visibleSuggestions(c: CalibChannel): CalibSuggestion[] {
  return props.readonly
    ? c.suggestions.filter((s) => included(s.id))
    : c.suggestions;
}
// Only channels that have a (visible) suggestion or a gap note are worth showing.
const channels = computed<CalibChannel[]>(
  () =>
    props.preview?.channels.filter(
      (c) => visibleSuggestions(c).length || (c.notes?.length ?? 0),
    ) ?? [],
);
const hasSuggestions = computed(() =>
  (props.preview?.channels ?? []).some((c) => visibleSuggestions(c).length > 0),
);

function included(id: string): boolean {
  return !excluded.value.includes(id);
}
function toggle(id: string, on: boolean) {
  const set = new Set(excluded.value);
  if (on) set.delete(id);
  else set.add(id);
  excluded.value = [...set];
}

// Single "select all / deselect all" control over every matched master. Every suggestion id across
// all channels; a master is "on" (applied) when it is NOT in the excluded list.
const allIds = computed<string[]>(() =>
  channels.value.flatMap((c) => c.suggestions.map((s) => s.id)),
);
const allOn = computed(
  () => allIds.value.length > 0 && allIds.value.every((id) => included(id)),
);
const noneOn = computed(() => allIds.value.every((id) => !included(id)));
function toggleAll(on: boolean) {
  if (on) {
    excluded.value = excluded.value.filter((id) => !allIds.value.includes(id));
  } else {
    excluded.value = [...new Set([...excluded.value, ...allIds.value])];
  }
}

// Vue can't bind the tri-state `indeterminate` property declaratively — drive it from a ref.
const allBox = ref<HTMLInputElement | null>(null);
watchEffect(() => {
  if (allBox.value) allBox.value.indeterminate = !allOn.value && !noneOn.value;
});

function channelLine(c: CalibChannel): string {
  const parts = [
    humanizeMs(c.exposure_ms),
    `gain ${c.gain}`,
    tempC(c.temp_bucket_c * 1000),
  ];
  // A multi-night scan splits one filter into per-night channels — the date is what tells them apart.
  if (c.session) parts.unshift(c.session);
  return parts.join(" · ");
}
function masterLine(s: CalibSuggestion): string {
  const m = s.master;
  const parts = [t("calib.frames", { n: m.frame_count })];
  if (s.role === "dark") {
    if (m.temp_milli_c) parts.push(tempC(m.temp_milli_c));
    if (m.exposure_ms) parts.push(humanizeMs(m.exposure_ms));
  } else if (s.role === "flat" && m.filter) {
    parts.push(m.filter);
  }
  return parts.join(" · ");
}
// Fuller param line for the read-only job view, so "what params" is explicit: everything the matched
// master carries (frames · exposure · temp · gain · offset · bin · filter, whichever it has).
function masterLineFull(s: CalibSuggestion): string {
  const m = s.master;
  const parts = [t("calib.frames", { n: m.frame_count })];
  if (m.exposure_ms) parts.push(humanizeMs(m.exposure_ms));
  if (m.temp_milli_c) parts.push(tempC(m.temp_milli_c));
  parts.push(`gain ${m.gain}`);
  if (m.offset) parts.push(`offset ${m.offset}`);
  if (m.bin > 1) parts.push(`bin ${m.bin}`);
  if (s.role === "flat" && m.filter) parts.push(m.filter);
  return parts.join(" · ");
}
function channelKey(c: CalibChannel): string {
  return `${c.filter}|${c.exposure_ms}|${c.gain}|${c.offset}|${c.bin}|${c.temp_bucket_c}|${c.session ?? ""}`;
}
</script>

<template>
  <div v-if="hasSuggestions" :class="card">
    <h3 class="font-semibold">
      {{ readonly ? t("calib.includedTitle") : t("calib.title") }}
    </h3>
    <p class="text-xs text-slate-500 dark:text-slate-400">
      {{ readonly ? t("calib.includedSubtitle") : t("calib.subtitle") }}
    </p>

    <label
      v-if="!readonly"
      class="mt-2 flex w-max cursor-pointer items-center gap-2 text-xs font-medium text-slate-600 dark:text-slate-300"
    >
      <input
        ref="allBox"
        type="checkbox"
        class="accent-brand-500"
        :checked="allOn"
        @change="toggleAll(($event.target as HTMLInputElement).checked)"
      />
      {{ allOn ? t("calib.deselectAll") : t("calib.selectAll") }}
    </label>

    <ul class="mt-3 space-y-3">
      <li v-for="c in channels" :key="channelKey(c)">
        <div class="mb-1 flex items-center gap-2 text-sm">
          <FilterChip v-if="c.filter" :filter="c.filter" />
          <span v-else class="text-slate-400">—</span>
          <span class="text-xs text-slate-500 dark:text-slate-400">{{
            channelLine(c)
          }}</span>
        </div>
        <div class="space-y-0.5 pl-1">
          <component
            :is="readonly ? 'div' : 'label'"
            v-for="s in visibleSuggestions(c)"
            :key="s.id"
            class="flex items-center gap-2 rounded px-2 py-1 text-sm"
            :class="
              readonly
                ? ''
                : 'cursor-pointer hover:bg-slate-100 dark:hover:bg-slate-800'
            "
          >
            <input
              v-if="!readonly"
              type="checkbox"
              class="accent-brand-500"
              :checked="included(s.id)"
              @change="
                toggle(s.id, ($event.target as HTMLInputElement).checked)
              "
            />
            <span
              class="w-10 font-medium"
              :class="frameTypeAccentClass(s.role.toUpperCase())"
            >
              {{ t("calib.role." + s.role) }}
            </span>
            <span class="text-slate-500 dark:text-slate-400">{{
              readonly ? masterLineFull(s) : masterLine(s)
            }}</span>
            <span
              class="ml-auto shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
              :class="
                s.from_capture
                  ? 'bg-brand-500/10 text-brand-500'
                  : 'bg-slate-500/10 text-slate-400 dark:text-slate-500'
              "
            >
              {{
                t(
                  s.from_capture
                    ? "calib.source.capture"
                    : "calib.source.library",
                )
              }}
            </span>
          </component>
          <p
            v-for="(n, i) in c.notes"
            :key="i"
            class="pl-2 text-xs text-warning"
          >
            ⚠ {{ n }}
          </p>
        </div>
      </li>
    </ul>
  </div>
</template>
