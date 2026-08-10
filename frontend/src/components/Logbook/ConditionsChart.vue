<script setup lang="ts">
import { computed } from "vue";
import { use } from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkAreaComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import {
  CHART_AXIS,
  CHART_GRID,
  CHART_ALT_LINE,
  CHART_MOON,
  CHART_SUN,
  CHART_DARK_BAND,
} from "@/constants/colors";
import { fmtClock } from "@/utils/tz";
import type { CaptureConditionRow, CaptureForecastRow } from "@/types";

use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkAreaComponent,
  CanvasRenderer,
]);

// How the sky moved over the session.
//
// Percentages and altitude share the left axis (both are 0–100-ish and comparable at a glance);
// temperature and seeing get the right one, because their units and ranges have nothing in common
// with a percentage and overlaying them on the same scale would flatten both into noise.
//
// A gap in the data is drawn as a GAP, never interpolated: an hour whose feeds were down must not be
// smoothed into a plausible-looking clear sky.
const props = defineProps<{
  rows: CaptureConditionRow[];
  forecast?: CaptureForecastRow | null;
  showForecast?: boolean;
  // Observer-site IANA timezone for the axis clock. The site is stored on the session, so a run shot
  // on a trip still reads in the hours it was actually shot at.
  tz: string;
}>();
const { t } = useI18n();

const CLOUD = "#94a3b8";
const HUMIDITY = "#38bdf8";
const TRANSPARENCY = "#22c55e";
const SEEING = "#f59e0b";

// A time axis needs [x, y] pairs, and a null y is what draws a real GAP rather than a straight line
// bridging an hour nobody measured.
type Point = [number, number | null];

const times = computed(() => props.rows.map((r) => r.at_ms));

// pointsOf zips the sample times with one metric. Rows whose feeds were entirely down are blanked —
// otherwise a dead feed draws a flat 0 % cloud line, which reads as a perfect night. Metrics the
// weather package documents as "0 = not supplied" (seeing, transparency) pass absentIsZero.
function pointsOf(
  pick: (r: CaptureConditionRow) => number,
  opts: { needsWeather?: boolean; absentIsZero?: boolean } = {},
): Point[] {
  const { needsWeather = true, absentIsZero = false } = opts;
  return props.rows.map((r) => {
    if (needsWeather && r.source === "unavailable") return [r.at_ms, null];
    const v = pick(r);
    return [r.at_ms, absentIsZero && v <= 0 ? null : v];
  });
}

const series = computed(() => {
  const out: Record<string, unknown>[] = [
    {
      name: t("logbook.chart.cloud"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: props.rows.length < 30,
      connectNulls: false,
      lineStyle: { color: CLOUD, width: 2 },
      itemStyle: { color: CLOUD },
      areaStyle: { color: CLOUD, opacity: 0.15 },
      data: pointsOf((r) => r.cloud_pct),
    },
    {
      name: t("logbook.chart.humidity"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { color: HUMIDITY, width: 1.5 },
      itemStyle: { color: HUMIDITY },
      data: pointsOf((r) => r.humidity_pct),
    },
    {
      name: t("logbook.chart.transparency"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { color: TRANSPARENCY, width: 1.5 },
      itemStyle: { color: TRANSPARENCY },
      data: pointsOf((r) => r.transparency * 100, { absentIsZero: true }),
    },
    {
      name: t("logbook.chart.temp"),
      type: "line",
      yAxisIndex: 1,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { color: CHART_SUN, width: 1.5, type: "dashed" },
      itemStyle: { color: CHART_SUN },
      data: pointsOf((r) => r.temp_c),
    },
    {
      name: t("logbook.chart.seeing"),
      type: "line",
      yAxisIndex: 1,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { color: SEEING, width: 1.5 },
      itemStyle: { color: SEEING },
      data: pointsOf((r) => r.seeing_arcsec, { absentIsZero: true }),
    },
  ];

  // Only worth plotting when the run knew where it pointed; otherwise every value is a zero.
  if (props.rows.some((r) => r.target_valid)) {
    out.push({
      name: t("logbook.chart.targetAlt"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: false,
      connectNulls: false,
      lineStyle: { color: CHART_ALT_LINE, width: 2 },
      itemStyle: { color: CHART_ALT_LINE },
      data: props.rows.map(
        (r): Point => [r.at_ms, r.target_valid ? r.target_alt_deg : null],
      ),
    });
  }
  if (props.rows.some((r) => r.moon_alt_deg > 0)) {
    out.push({
      name: t("logbook.chart.moonAlt"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: false,
      lineStyle: { color: CHART_MOON, width: 1.5, type: "dotted" },
      itemStyle: { color: CHART_MOON },
      data: pointsOf((r) => r.moon_alt_deg, { needsWeather: false }),
    });
  }

  // What was FORECAST when the session started, against what was then measured hour by hour. This is
  // the whole reason the full timeline is archived rather than just the samples.
  if (props.showForecast && props.forecast?.payload?.hours?.length) {
    const byTime = new Map(
      props.forecast.payload.hours.map((h) => [h.t_ms, h.cloud_pct]),
    );
    const hours = [...byTime.keys()].sort((a, b) => a - b);
    out.push({
      name: t("logbook.chart.cloudForecast"),
      type: "line",
      yAxisIndex: 0,
      showSymbol: false,
      lineStyle: { color: CLOUD, width: 1.5, type: "dashed", opacity: 0.8 },
      itemStyle: { color: CLOUD },
      // Its own x values: the forecast is hourly on the hour, the samples are not.
      data: hours.map((ms): Point => [ms, byTime.get(ms) ?? null]),
    });
  }
  return out;
});

// A paused stretch is usually the observer waiting out cloud, so it is worth marking.
const pausedBands = computed(() => {
  const bands: { xAxis: number }[][] = [];
  let start: number | null = null;
  for (const r of props.rows) {
    if (r.session_status === "paused" && start === null) start = r.at_ms;
    if (r.session_status !== "paused" && start !== null) {
      bands.push([{ xAxis: start }, { xAxis: r.at_ms }]);
      start = null;
    }
  }
  if (start !== null && props.rows.length) {
    bands.push([
      { xAxis: start },
      { xAxis: props.rows[props.rows.length - 1].at_ms },
    ]);
  }
  return bands;
});

const option = computed(() => ({
  tooltip: {
    trigger: "axis",
    axisPointer: { type: "line" },
  },
  legend: { textStyle: { color: CHART_AXIS }, type: "scroll" },
  grid: { left: 48, right: 52, top: 40, bottom: 32 },
  xAxis: {
    type: "time",
    min: times.value[0],
    max: times.value[times.value.length - 1],
    axisLabel: {
      color: CHART_AXIS,
      formatter: (ms: number) => fmtClock(ms, props.tz),
    },
    axisLine: { lineStyle: { color: CHART_GRID } },
    splitLine: { show: false },
  },
  yAxis: [
    {
      type: "value",
      name: t("logbook.chart.axisPercent"),
      nameTextStyle: { color: CHART_AXIS, fontSize: 10 },
      min: 0,
      max: 100,
      axisLabel: { color: CHART_AXIS },
      splitLine: { lineStyle: { color: CHART_GRID, opacity: 0.4 } },
    },
    {
      type: "value",
      name: t("logbook.chart.axisValue"),
      nameTextStyle: { color: CHART_AXIS, fontSize: 10 },
      axisLabel: { color: CHART_AXIS },
      splitLine: { show: false },
    },
  ],
  series: series.value.map((s, i) =>
    i === 0
      ? {
          ...s,
          markArea: {
            silent: true,
            itemStyle: { color: CHART_DARK_BAND },
            data: pausedBands.value,
          },
        }
      : s,
  ),
}));
</script>

<template>
  <VChart
    data-demo="logbook-conditions"
    v-if="rows.length > 1"
    class="h-72 w-full"
    :option="option"
    autoresize
  />
  <p v-else class="py-8 text-center text-sm text-slate-400">
    {{ t("logbook.chart.tooShort") }}
  </p>
</template>
