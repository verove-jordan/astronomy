<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { card, frameTypeAccentClass } from "@/constants/styles";
import { humanizeMs, tempC } from "@/utils/format";
import FilterChip from "@/components/Common/FilterChip.vue";
import type { CalibPreview, CalibChannel, CalibSuggestion } from "@/types";

const props = defineProps<{ preview: CalibPreview | null }>();
const { t } = useI18n();

// Two-way: the suggestion ids the user excluded (unchecked) — threaded into the run as calib_exclude.
const excluded = defineModel<string[]>("excluded", { default: () => [] });

// Only channels that have a suggestion or a gap note are worth showing.
const channels = computed<CalibChannel[]>(
  () =>
    props.preview?.channels.filter(
      (c) => c.suggestions.length || (c.notes?.length ?? 0),
    ) ?? [],
);
const hasSuggestions = computed(() =>
  (props.preview?.channels ?? []).some((c) => c.suggestions.length > 0),
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

function channelLine(c: CalibChannel): string {
  return [
    humanizeMs(c.exposure_ms),
    `gain ${c.gain}`,
    tempC(c.temp_bucket_c * 1000),
  ].join(" · ");
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
function channelKey(c: CalibChannel): string {
  return `${c.filter}|${c.exposure_ms}|${c.gain}|${c.offset}|${c.bin}|${c.temp_bucket_c}`;
}
</script>

<template>
  <div v-if="hasSuggestions" :class="card">
    <h3 class="font-semibold">{{ t("calib.title") }}</h3>
    <p class="text-xs text-slate-500 dark:text-slate-400">
      {{ t("calib.subtitle") }}
    </p>

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
          <label
            v-for="s in c.suggestions"
            :key="s.id"
            class="flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
          >
            <input
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
              masterLine(s)
            }}</span>
          </label>
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
