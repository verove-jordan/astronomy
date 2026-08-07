<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import FilterChip from "@/components/Common/FilterChip.vue";
import { humanizeMs, tempC } from "@/utils/format";
import { isLight } from "@/utils/logbook";
import { td, th } from "@/constants/styles";
import type { CaptureFrameStat } from "@/types";

// What was actually shot, per filter and frame type: the tally the rest of the night is planned from
// and the one a later stack is judged by. Every column comes from the frame rows, because only they
// know the exposure, gain, binning and sensor temperature each filter was really shot at.
const props = defineProps<{ stats: CaptureFrameStat[] }>();
const { t } = useI18n();

// Lights first — they are what the observer is counting — then calibration, each alphabetically.
const rows = computed(() =>
  [...props.stats].sort((a, b) => {
    const la = isLight(a.frame_type) ? 0 : 1;
    const lb = isLight(b.frame_type) ? 0 : 1;
    return (
      la - lb ||
      a.frame_type.localeCompare(b.frame_type) ||
      a.filter.localeCompare(b.filter)
    );
  }),
);

// A range collapses to a single value when both ends agree, which is the normal case — showing
// "100 – 100" everywhere would bury the one row where the gain actually changed mid-run.
function range(min: number, max: number, fmt: (v: number) => string): string {
  return min === max ? fmt(min) : `${fmt(min)} – ${fmt(max)}`;
}

const secs = (us: number) =>
  `${(us / 1_000_000).toFixed(us < 1_000_000 ? 2 : 0)} s`;
const num = (v: number) => String(v);
</script>

<template>
  <div class="overflow-x-auto">
    <table class="min-w-full">
      <thead>
        <tr>
          <th :class="th">{{ t("logbook.tally.filter") }}</th>
          <th :class="th">{{ t("logbook.tally.type") }}</th>
          <th :class="th">{{ t("logbook.tally.frames") }}</th>
          <th :class="th">{{ t("logbook.tally.integration") }}</th>
          <th :class="th">{{ t("logbook.tally.exposure") }}</th>
          <th :class="th">{{ t("logbook.tally.gain") }}</th>
          <th :class="th">{{ t("logbook.tally.bin") }}</th>
          <th :class="th">{{ t("logbook.tally.temp") }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="r in rows"
          :key="`${r.frame_type}-${r.filter}`"
          class="border-t border-slate-100 dark:border-slate-700/60"
        >
          <td :class="td">
            <FilterChip v-if="r.filter" :filter="r.filter" />
            <span v-else class="text-slate-400">—</span>
          </td>
          <td :class="td">{{ r.frame_type }}</td>
          <td :class="[td, 'font-mono tabular-nums']">{{ r.frames }}</td>
          <td :class="[td, 'font-mono tabular-nums']">
            {{ humanizeMs(r.total_exposure_us / 1000) }}
          </td>
          <td :class="[td, 'font-mono tabular-nums']">
            {{ range(r.min_exposure_us, r.max_exposure_us, secs) }}
          </td>
          <td :class="[td, 'font-mono tabular-nums']">
            {{ range(r.min_gain, r.max_gain, num) }}
          </td>
          <td :class="[td, 'font-mono tabular-nums']">
            {{ range(r.min_bin, r.max_bin, num) }}
          </td>
          <!-- The average matters more than the extremes here: a cooled camera settles, and the
               spread is what says whether it had settled before the run started. -->
          <td :class="[td, 'font-mono tabular-nums']">
            {{ tempC(r.avg_temp_milli_c) }}
            <span
              v-if="r.min_temp_milli_c !== r.max_temp_milli_c"
              class="text-xs text-slate-400"
            >
              ({{ tempC(r.min_temp_milli_c) }} …
              {{ tempC(r.max_temp_milli_c) }})
            </span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
