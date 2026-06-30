<script setup lang="ts">
import { computed } from "vue";
import { use } from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
  MarkPointComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import VChart from "vue-echarts";
import { useI18n } from "vue-i18n";
import type { SkyTarget, DarkWindow, AltSample } from "@/types";
import { fmtClock } from "@/utils/tz";
import {
  CHART_AXIS,
  CHART_GRID,
  CHART_ALT_LINE,
  CHART_ALT_FILL,
  CHART_DARK_BAND,
  CHART_HORIZON,
  CHART_MINALT,
  CHART_TRANSIT,
  CHART_NOW,
  CHART_SUN,
  CHART_MOON,
} from "@/constants/colors";

use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
  MarkPointComponent,
  CanvasRenderer,
]);

// Night chart: Sun + Moon altitude over the night (always), with the selected object overlaid. Shows
// the astronomical dark band and sunset/sunrise/moonrise/moonset + now markers.
const props = defineProps<{
  target: SkyTarget | null;
  darkWindow: DarkWindow;
  minAltDeg: number;
  nowMs: number;
  tz: string; // observer-site IANA timezone for axis/tooltip times
}>();
const { t } = useI18n();

const xy = (s: AltSample[]): [number, number][] =>
  (s ?? []).map((p) => [p.t_ms, p.alt_deg]);

// ECharts option/series are loosely typed; we build a plain object literal.
/* eslint-disable @typescript-eslint/no-explicit-any */
const option = computed(() => {
  const dw = props.darkWindow;
  // Vertical event lines (sunset/now/…) carry their label rotated 90° ALONG the line, in the line's
  // own colour, so neighbouring markers never overlap each other, the axis name or the legend.
  const vlabel = (color: string, formatter: string) => ({
    formatter,
    color,
    rotate: 90,
    position: "end" as const,
    align: "left" as const,
    verticalAlign: "middle" as const,
    fontSize: 10,
    distance: 5,
  });

  const sunRiseSet: any[] = [];
  if (dw.sun?.set_utc_ms)
    sunRiseSet.push({
      xAxis: dw.sun.set_utc_ms,
      lineStyle: { color: CHART_SUN, type: "dashed" },
      label: vlabel(CHART_SUN, t("tonight.chart.sunset")),
    });
  if (dw.sun?.rise_utc_ms)
    sunRiseSet.push({
      xAxis: dw.sun.rise_utc_ms,
      lineStyle: { color: CHART_SUN, type: "dashed" },
      label: vlabel(CHART_SUN, t("tonight.chart.sunrise")),
    });

  const moonRiseSet: any[] = [];
  if (dw.moon?.rise_utc_ms)
    moonRiseSet.push({
      xAxis: dw.moon.rise_utc_ms,
      lineStyle: { color: CHART_MOON, type: "dotted" },
      label: vlabel(CHART_MOON, t("tonight.chart.moonrise")),
    });
  if (dw.moon?.set_utc_ms)
    moonRiseSet.push({
      xAxis: dw.moon.set_utc_ms,
      lineStyle: { color: CHART_MOON, type: "dotted" },
      label: vlabel(CHART_MOON, t("tonight.chart.moonset")),
    });

  const series: any[] = [
    {
      name: t("tonight.chart.sun"),
      type: "line",
      showSymbol: false,
      smooth: true,
      data: xy(dw.sun_series),
      lineStyle: { color: CHART_SUN, width: 1.5 },
      itemStyle: { color: CHART_SUN },
      markArea: {
        silent: true,
        itemStyle: { color: CHART_DARK_BAND },
        data: [[{ xAxis: dw.dusk_utc_ms }, { xAxis: dw.dawn_utc_ms }]],
      },
      markLine: {
        symbol: "none",
        data: [
          {
            yAxis: 0,
            lineStyle: { color: CHART_HORIZON },
            label: {
              formatter: t("tonight.chart.horizon"),
              color: CHART_HORIZON,
              position: "insideStartTop",
              fontSize: 10,
            },
          },
          {
            xAxis: props.nowMs,
            lineStyle: { color: CHART_NOW },
            label: vlabel(CHART_NOW, t("tonight.chart.now")),
          },
          ...sunRiseSet,
        ],
      },
    },
    {
      name: t("tonight.chart.moon"),
      type: "line",
      showSymbol: false,
      smooth: true,
      data: xy(dw.moon_series),
      lineStyle: { color: CHART_MOON, width: 1.5, type: "dashed" },
      itemStyle: { color: CHART_MOON },
      markLine: { symbol: "none", data: moonRiseSet },
    },
  ];

  if (props.target) {
    const s = props.target;
    series.push({
      name: s.name,
      type: "line",
      showSymbol: false,
      smooth: true,
      data: xy(s.alt_series ?? []),
      lineStyle: { color: CHART_ALT_LINE, width: 2.5 },
      itemStyle: { color: CHART_ALT_LINE },
      areaStyle: { color: CHART_ALT_FILL, opacity: 0.15 },
      markLine: {
        symbol: "none",
        data: [
          {
            yAxis: props.minAltDeg,
            lineStyle: { color: CHART_MINALT, type: "dashed" },
            label: {
              formatter: t("tonight.chart.minAlt"),
              color: CHART_MINALT,
              position: "insideEndTop",
              fontSize: 10,
            },
          },
          {
            xAxis: s.transit_utc_ms,
            lineStyle: { color: CHART_TRANSIT, type: "dotted" },
            label: vlabel(CHART_TRANSIT, t("tonight.chart.transit")),
          },
        ],
      },
      markPoint: {
        symbolSize: 8,
        itemStyle: { color: CHART_TRANSIT },
        label: { color: "#fff", fontSize: 10 },
        data: [
          {
            coord: [s.transit_utc_ms, s.max_alt_deg],
            value: Math.round(s.max_alt_deg),
          },
        ],
      },
    });
  }

  return {
    grid: { left: 54, right: 18, top: 40, bottom: 28 },
    legend: { top: 6, textStyle: { color: CHART_AXIS } },
    tooltip: {
      trigger: "axis",
      formatter: (params: any) => {
        const arr = Array.isArray(params) ? params : [params];
        const rows = arr
          .map(
            (p: any) =>
              `${p.marker} ${p.seriesName}: ${Math.round(p.value[1])}°`,
          )
          .join("<br/>");
        return `${fmtClock(arr[0].axisValue, props.tz)}<br/>${rows}`;
      },
    },
    xAxis: {
      type: "time",
      min: dw.night_start_ms,
      max: dw.night_end_ms,
      axisLabel: {
        color: CHART_AXIS,
        hideOverlap: true,
        formatter: (v: number) => fmtClock(v, props.tz),
      },
      axisLine: { lineStyle: { color: CHART_AXIS } },
    },
    yAxis: {
      type: "value",
      min: 0,
      max: 90,
      name: t("tonight.chart.altitude"),
      nameLocation: "middle",
      nameGap: 38,
      nameRotate: 90,
      nameTextStyle: { color: CHART_AXIS },
      axisLabel: { color: CHART_AXIS, formatter: "{value}°" },
      splitLine: { lineStyle: { color: CHART_GRID } },
    },
    series,
  };
});
/* eslint-enable @typescript-eslint/no-explicit-any */
</script>

<template>
  <VChart :option="option" autoresize class="h-80 w-full" />
</template>
