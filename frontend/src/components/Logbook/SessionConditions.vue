<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { card } from "@/constants/styles";
import { dewRiskColor, auroraColor, verdictColor } from "@/utils/weather";
import { moonPhaseKey, fmtDeg, fmtOne } from "@/utils/logbook";
import type { ConditionsSummary } from "@/types";

// The night at a glance: the numbers a later stacking decision is actually made from.
//
// Every metric carries its sample count, because a median drawn from two readings out of twelve is
// not the same claim as one drawn from all twelve — and a night whose feeds were down must not look
// like a night that was flawlessly clear.
const props = defineProps<{ summary: ConditionsSummary }>();
const { t } = useI18n();

interface Cell {
  key: string;
  label: string;
  value: string;
  sub?: string;
  color?: string;
}

// dash renders a metric that was never supplied, rather than a zero that reads as a real reading.
function stat(
  key: string,
  fmt: (v: number) => string,
  pick: (s: ConditionsSummary) => {
    median: number;
    min: number;
    max: number;
    n: number;
  },
): Cell {
  const s = pick(props.summary);
  if (!s || s.n === 0) {
    return {
      key,
      label: t(`logbook.conditions.${key}`),
      value: "—",
      sub: t("logbook.conditions.notSupplied"),
    };
  }
  return {
    key,
    label: t(`logbook.conditions.${key}`),
    value: fmt(s.median),
    sub: s.min === s.max ? undefined : `${fmt(s.min)} … ${fmt(s.max)}`,
  };
}

const pct = (v: number) => `${Math.round(v)} %`;
const arcsec = (v: number) => `${fmtOne(v)}″`;
const deg = (v: number) => `${fmtOne(v)} °C`;
const kmh = (v: number) => `${Math.round(v)} km/h`;

const weatherCells = computed<Cell[]>(() => [
  stat("cloud", pct, (s) => s.cloud_pct),
  stat("seeing", arcsec, (s) => s.seeing_arcsec),
  stat(
    "transparency",
    (v) => `${Math.round(v * 100)} %`,
    (s) => s.transparency,
  ),
  stat("humidity", pct, (s) => s.humidity_pct),
  stat("temp", deg, (s) => s.temp_c),
  stat("wind", kmh, (s) => s.wind_kmh),
]);

const verdict = computed(() => props.summary.verdict);

const moonPhaseLabel = computed(() =>
  t(`tonight.moonPhase.${moonPhaseKey(props.summary.moon_phase_angle_deg)}`),
);

// The Moon's worst moment, not its average: a Moon that rose at 03:00 ruined the last hour even
// though it spent most of the night below the horizon.
const moonCells = computed<Cell[]>(() => {
  const s = props.summary;
  const cells: Cell[] = [
    {
      key: "phase",
      label: t("logbook.conditions.moonPhase"),
      value: moonPhaseLabel.value,
      sub: `${Math.round(s.moon_illum_max * 100)} %`,
    },
    {
      key: "moonAlt",
      label: t("logbook.conditions.moonAlt"),
      value: s.moon_up
        ? fmtDeg(s.moon_alt_max_deg)
        : t("logbook.conditions.moonDown"),
      sub: s.moon_up ? t("logbook.conditions.highest") : undefined,
    },
  ];
  if (s.target_valid) {
    cells.push({
      key: "moonSep",
      label: t("logbook.conditions.moonSep"),
      value: fmtDeg(s.moon_sep_min_deg),
      sub: t("logbook.conditions.closest"),
    });
  }
  return cells;
});

const targetCells = computed<Cell[]>(() => {
  const s = props.summary;
  if (!s.target_valid) return [];
  return [
    {
      key: "targetAlt",
      label: t("logbook.conditions.targetAlt"),
      value: fmtDeg(s.target_alt_max_deg),
      sub: `${fmtDeg(s.target_alt_min_deg)} … ${fmtDeg(s.target_alt_max_deg)}`,
    },
    {
      key: "airmass",
      label: t("logbook.conditions.airmass"),
      // 0 means the target never cleared the horizon while sampling; showing "1.00" would be a lie.
      value: s.target_airmass_min > 0 ? s.target_airmass_min.toFixed(2) : "—",
      sub: s.target_airmass_min > 0 ? t("logbook.conditions.best") : undefined,
    },
  ];
});

const siteCells = computed<Cell[]>(() => {
  const s = props.summary;
  const cells: Cell[] = [];
  if (s.sqm > 0) {
    cells.push({
      key: "sky",
      label: t("logbook.conditions.skyBrightness"),
      value: `${s.sqm.toFixed(2)}`,
      sub: `Bortle ${s.bortle}`,
    });
  }
  if (s.dew_risk_worst) {
    cells.push({
      key: "dew",
      label: t("logbook.conditions.dewRisk"),
      value: t(`tonight.weather.dewRisk.${s.dew_risk_worst}`),
      color: dewRiskColor(s.dew_risk_worst),
    });
  }
  if (s.kp_max > 0) {
    cells.push({
      key: "kp",
      label: t("logbook.conditions.aurora"),
      value: `Kp ${fmtOne(s.kp_max)}`,
      sub: s.aurora_max || undefined,
      color: auroraColor(s.aurora_max),
    });
  }
  return cells;
});

// How much of the record is real. A night whose feeds were all down still has correct Moon and
// target geometry — that half is computed locally — so saying so is more useful than hiding it.
const unavailable = computed(
  () => props.summary.source_counts?.unavailable ?? 0,
);
</script>

<template>
  <div class="space-y-4">
    <div :class="[card, 'flex flex-wrap items-center gap-x-6 gap-y-2']">
      <div>
        <p class="text-xs uppercase tracking-wide text-slate-500">
          {{ t("logbook.conditions.verdict") }}
        </p>
        <p
          class="text-2xl font-semibold"
          :style="{
            color: verdict.n ? verdictColor(verdict.median) : undefined,
          }"
        >
          {{ verdict.n ? Math.round(verdict.median) : "—" }}
          <span v-if="verdict.n" class="text-sm font-normal text-slate-400"
            >/ 100</span
          >
        </p>
      </div>
      <p class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("logbook.conditions.samples", { n: summary.samples }) }}
        <span v-if="unavailable" class="text-amber-600 dark:text-amber-400">
          · {{ t("logbook.conditions.feedsDown", { n: unavailable }) }}
        </span>
      </p>
    </div>

    <section
      v-for="group in [
        { key: 'weather', cells: weatherCells },
        { key: 'moon', cells: moonCells },
        { key: 'target', cells: targetCells },
        { key: 'site', cells: siteCells },
      ]"
      :key="group.key"
    >
      <template v-if="group.cells.length">
        <h3 class="mb-2 text-sm font-medium text-slate-600 dark:text-slate-300">
          {{ t(`logbook.conditions.groups.${group.key}`) }}
        </h3>
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
          <div v-for="c in group.cells" :key="c.key" :class="[card, '!p-3']">
            <p
              class="truncate text-[11px] uppercase tracking-wide text-slate-500"
            >
              {{ c.label }}
            </p>
            <p
              class="text-lg font-semibold tabular-nums"
              :style="{ color: c.color }"
            >
              {{ c.value }}
            </p>
            <p v-if="c.sub" class="truncate text-[11px] text-slate-400">
              {{ c.sub }}
            </p>
          </div>
        </div>
      </template>
    </section>
  </div>
</template>
