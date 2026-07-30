<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { SkyEvent } from "@/types";
import { card, scoreTier, scoreTierBar } from "@/constants/styles";
import Pill from "@/components/Common/Pill.vue";
import ScoreBadge from "@/components/Common/ScoreBadge.vue";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import AltitudeChart from "@/components/Dataviz/AltitudeChart.vue";
import EventIcon from "@/components/Events/EventIcon.vue";
import { fmtDateTime, fmtClock } from "@/utils/tz";
import { eventTitle, kindLabel, kindPillClass } from "@/utils/events";

const props = defineProps<{
  event: SkyEvent | null;
  tz: string;
  limits?: { naked_eye: number; binocular: number; telescope: number };
}>();
const { t } = useI18n();

// fmtSec formats a UTC ms with seconds in the site tz — transits need sub-minute capture timing.
function fmtSec(ms: number): string {
  return new Intl.DateTimeFormat("en-GB", {
    timeZone: props.tz,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(new Date(ms));
}

const title = computed(() => (props.event ? eventTitle(props.event, t) : ""));

function contactLabel(label: string): string {
  return t("calendar.contact." + label);
}

// The "why this score" factor bars come straight from the backend (rarity / altitude / Moon / …).
const factors = computed(() => props.event?.score_factors ?? []);

function factorLabel(key: string): string {
  return t("calendar.factor." + key);
}

// One-line takeaway: name the weakest (limiting) factor, or note it's well placed.
const scoreSummary = computed(() => {
  const f = factors.value;
  if (!f.length) return "";
  const weakest = [...f].sort((a, b) => a.weight - b.weight)[0];
  return weakest.weight >= 0.6
    ? t("calendar.detail.scoreGood")
    : t("calendar.detail.scoreLimited", {
        factor: factorLabel(weakest.key).toLowerCase(),
      });
});

// Per-instrument bars, annotated with the tier's magnitude limit and why a tier scores zero.
const visBars = computed(() => {
  const e = props.event;
  if (!e) return [];
  const v = e.visibility;
  const lim = props.limits;
  const note = (val: number, limit?: number): string => {
    if (val > 0) return "";
    if (
      e.has_mag &&
      e.magnitude != null &&
      limit != null &&
      e.magnitude > limit
    )
      return t("calendar.detail.tooFaint");
    return t("calendar.detail.belowHorizon");
  };
  return [
    {
      label: t("calendar.vis.nakedEye"),
      val: v.naked_eye,
      limit: lim?.naked_eye,
      note: note(v.naked_eye, lim?.naked_eye),
    },
    {
      label: t("calendar.vis.binocular"),
      val: v.binocular,
      limit: lim?.binocular,
      note: note(v.binocular, lim?.binocular),
    },
    {
      label: t("calendar.vis.scope"),
      val: v.telescope,
      limit: lim?.telescope,
      note: note(v.telescope, lim?.telescope),
    },
  ];
});

const isTransit = computed(() => props.event?.kind === "satellite_transit");

// Show the night altitude chart only when the event's peak falls inside that night's chart window.
const showChart = computed(() => {
  const e = props.event;
  return (
    !!e?.night &&
    e.peak_utc_ms >= e.night.night_start_ms &&
    e.peak_utc_ms <= e.night.night_end_ms
  );
});
</script>

<template>
  <div :class="card">
    <p v-if="!event" class="text-sm text-slate-400">
      {{ t("calendar.detail.select") }}
    </p>
    <div v-else class="space-y-3">
      <div class="flex flex-wrap items-center gap-2">
        <h3 class="text-base font-semibold text-slate-800 dark:text-slate-100">
          {{ title }}
        </h3>
        <Pill :color-class="kindPillClass(event.kind)">
          <EventIcon :kind="event.kind" class="h-3.5 w-3.5" />
          {{ kindLabel(event, t) }}
        </Pill>
        <ScoreBadge :score="event.score" />
      </div>

      <!-- When / capture timing -->
      <div class="text-sm text-slate-600 dark:text-slate-300">
        <span class="text-slate-500 dark:text-slate-400"
          >{{ t("calendar.detail.when") }}:</span
        >
        {{ fmtDateTime(event.peak_utc_ms, tz) }}
      </div>

      <!-- Eclipse contact times (local circumstances at the site, to the second) -->
      <div
        v-if="event.contacts && event.contacts.length"
        class="rounded-md bg-slate-50 p-2 text-xs dark:bg-slate-800/50"
      >
        <div class="font-medium text-slate-600 dark:text-slate-300">
          {{ t("calendar.detail.captureTimes") }}
        </div>
        <div class="mt-1 space-y-0.5">
          <div
            v-for="c in event.contacts"
            :key="c.label"
            class="flex justify-between gap-3"
          >
            <span class="text-slate-500">{{ contactLabel(c.label) }}</span>
            <span class="tabular-nums text-slate-600 dark:text-slate-300"
              >{{ fmtSec(c.utc_ms) }} · {{ Math.round(c.alt_deg) }}°</span
            >
          </div>
        </div>
      </div>

      <!-- Satellite transit timing -->
      <div
        v-else-if="isTransit"
        class="rounded-md bg-slate-50 p-2 text-xs dark:bg-slate-800/50"
      >
        <div class="font-medium text-slate-600 dark:text-slate-300">
          {{ t("calendar.detail.captureTimes") }}
        </div>
        <div class="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-slate-500">
          <span v-if="event.start_utc_ms"
            >⟶ {{ fmtSec(event.start_utc_ms) }}</span
          >
          <span class="font-semibold text-brand-600 dark:text-brand-300"
            >◎ {{ fmtSec(event.peak_utc_ms) }}</span
          >
          <span v-if="event.end_utc_ms">⟵ {{ fmtSec(event.end_utc_ms) }}</span>
          <span v-if="event.duration_ms"
            >{{ t("calendar.detail.duration") }}:
            {{ (event.duration_ms / 1000).toFixed(1) }} s</span
          >
        </div>
        <div
          class="mt-1"
          :class="
            event.in_path
              ? 'text-green-600 dark:text-green-400'
              : 'text-amber-600 dark:text-amber-400'
          "
        >
          {{
            event.in_path
              ? t("calendar.detail.inPath")
              : t("calendar.detail.notInPath")
          }}
        </div>
        <div class="mt-1 text-slate-400">
          {{ t("calendar.detail.transitNote") }}
        </div>
      </div>

      <!-- Generic observing window (when the object is up in darkness) -->
      <div
        v-else-if="event.start_utc_ms && event.end_utc_ms"
        class="text-sm text-slate-500 dark:text-slate-400"
      >
        {{
          t("calendar.detail.visibleRange", {
            from: fmtClock(event.start_utc_ms, tz),
            to: fmtClock(event.end_utc_ms, tz),
          })
        }}
      </div>

      <!-- Why this score: the factors that produced it -->
      <div v-if="factors.length" class="space-y-1">
        <div class="flex items-baseline justify-between gap-2">
          <span
            class="text-xs font-medium text-slate-500 dark:text-slate-400"
            >{{ t("calendar.detail.why") }}</span
          >
          <span class="text-[11px] text-slate-400">{{ scoreSummary }}</span>
        </div>
        <div v-for="f in factors" :key="f.key" class="flex items-center gap-2">
          <span class="w-28 shrink-0 text-xs text-slate-500">{{
            factorLabel(f.key)
          }}</span>
          <ProgressBar
            :percent="Math.round(f.weight * 100)"
            :bar-class="scoreTierBar[scoreTier(f.weight * 100)]"
          />
          <span
            class="w-16 shrink-0 text-right text-[11px] tabular-nums text-slate-400"
            >{{ f.detail }}</span
          >
        </div>
        <p class="text-[11px] text-slate-400">
          {{ t("calendar.detail.scoreFormula") }}
        </p>
      </div>

      <!-- Per-instrument visibility (with each tier's magnitude limit + why it's zero) -->
      <div class="space-y-1">
        <div class="text-xs font-medium text-slate-500 dark:text-slate-400">
          {{ t("calendar.detail.visibility") }}
        </div>
        <div
          v-for="b in visBars"
          :key="b.label"
          class="flex items-center gap-2"
        >
          <span class="w-28 shrink-0 text-xs text-slate-500"
            >{{ b.label
            }}<span
              v-if="event.has_mag && b.limit != null"
              class="text-slate-400"
            >
              ≤{{ b.limit }}</span
            ></span
          >
          <ProgressBar
            :percent="b.val"
            :bar-class="scoreTierBar[scoreTier(b.val)]"
          />
          <span
            class="w-16 shrink-0 text-right text-[11px] tabular-nums text-slate-500"
          >
            <span v-if="b.note" class="text-amber-600 dark:text-amber-400">{{
              b.note
            }}</span>
            <span v-else>{{ b.val }}</span>
          </span>
        </div>
      </div>

      <!-- Facts -->
      <dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
        <div v-if="event.has_mag" class="flex justify-between">
          <dt class="text-slate-500">{{ t("calendar.detail.magnitude") }}</dt>
          <dd class="tabular-nums">{{ event.magnitude?.toFixed(1) }}</dd>
        </div>
        <div v-if="event.separation_deg" class="flex justify-between">
          <dt class="text-slate-500">{{ t("calendar.detail.separation") }}</dt>
          <dd class="tabular-nums">{{ event.separation_deg.toFixed(2) }}°</dd>
        </div>
        <div class="flex justify-between">
          <dt class="text-slate-500">{{ t("calendar.detail.altitude") }}</dt>
          <dd class="tabular-nums">{{ Math.round(event.alt_at_best_deg) }}°</dd>
        </div>
        <div class="flex justify-between">
          <dt class="text-slate-500">{{ t("calendar.detail.moon") }}</dt>
          <dd class="tabular-nums">
            {{
              t("calendar.detail.moonIllum", {
                pct: Math.round(event.moon_illum * 100),
              })
            }}
          </dd>
        </div>
      </dl>

      <p v-if="event.extra_text" class="text-xs text-slate-400">
        {{ event.extra_text }}
      </p>

      <!-- The night, reusing the Tonight altitude chart (Sun/Moon + a marker at the event time) -->
      <div v-if="showChart && event.night">
        <div
          class="mb-1 text-xs font-medium text-slate-500 dark:text-slate-400"
        >
          {{ t("calendar.detail.chart") }}
        </div>
        <AltitudeChart
          :target="null"
          :dark-window="event.night"
          :min-alt-deg="0"
          :now-ms="event.peak_utc_ms"
          :tz="tz"
        />
      </div>
    </div>
  </div>
</template>
