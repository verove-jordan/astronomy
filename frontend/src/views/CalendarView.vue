<script setup lang="ts">
import { onMounted, computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useEventsStore } from "@/stores/events";
import Spinner from "@/components/Common/Spinner.vue";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import LocationPicker from "@/components/Sky/LocationPicker.vue";
import EventCalendar from "@/components/Events/EventCalendar.vue";
import EventDetailPanel from "@/components/Events/EventDetailPanel.vue";
import EventIcon from "@/components/Events/EventIcon.vue";
import EventsTable from "@/components/Events/EventsTable.vue";
import {
  card,
  input,
  btnGhost,
  segWrap,
  segBtn,
  segActive,
  segIdle,
} from "@/constants/styles";
import TabBar from "@/components/Common/TabBar.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import { tzForLocation, fmtClock, fmtDateTime } from "@/utils/tz";
import { kindPillClass } from "@/utils/events";
import type { SkyEvent } from "@/types";

const { t } = useI18n();
const store = useEventsStore();

// Restore the last mode (date window vs by-type series) on load.
onMounted(() =>
  store.mode === "series" ? store.fetchSeries() : store.fetch(),
);

const tz = computed(() => {
  const l = store.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

// --- mode toggle ---
function setMode(m: "window" | "series") {
  if (store.mode === m) return;
  if (m === "series") store.fetchSeries();
  else setRange(rangeDays.value);
}

// --- date-window presets (window mode) ---
const rangeDays = ref(90);
function pad(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}
function todayInTz(): string {
  return fmtDateTime(Date.now(), tz.value).slice(0, 10);
}
function addDays(day: string, n: number): string {
  const [y, m, d] = day.split("-").map(Number);
  const dt = new Date(Date.UTC(y, m - 1, d + n));
  return `${dt.getUTCFullYear()}-${pad(dt.getUTCMonth() + 1)}-${pad(dt.getUTCDate())}`;
}
function setRange(days: number) {
  rangeDays.value = days;
  const from = todayInTz();
  store.fetch({ from, to: addDays(from, days) }, true);
}

// --- by-type series (series mode) ---
const seriesKinds = [
  "solar_eclipse",
  "lunar_eclipse",
  "moon_phase",
  "supermoon",
  "equinox",
  "solstice",
  "meteor_shower",
  "opposition",
  "elongation",
  "conjunction",
  "planet_moon",
];
const SHOWERS: [string, string][] = [
  ["PER", "Perseids"],
  ["GEM", "Geminids"],
  ["QUA", "Quadrantids"],
  ["LYR", "Lyrids"],
  ["ETA", "Eta Aquariids"],
  ["SDA", "Delta Aquariids"],
  ["ORI", "Orionids"],
  ["LEO", "Leonids"],
  ["DRA", "Draconids"],
  ["URS", "Ursids"],
  ["NTA", "Northern Taurids"],
  ["STA", "Southern Taurids"],
];

function subtypeOptions(kind: string): { value: string; label: string }[] {
  const any = { value: "", label: t("calendar.series.any") };
  const ecl = (vs: string[]) =>
    vs.map((v) => ({ value: v, label: t("calendar.eclipseType." + v) }));
  switch (kind) {
    case "solar_eclipse":
      return [any, ...ecl(["total", "annular", "annular_total", "partial"])];
    case "lunar_eclipse":
      return [any, ...ecl(["total", "partial", "penumbral"])];
    case "moon_phase":
      return [
        any,
        ...["new", "first_quarter", "full", "last_quarter"].map((v) => ({
          value: v,
          label: t("calendar.phase." + v),
        })),
      ];
    case "meteor_shower":
      return [any, ...SHOWERS.map(([c, n]) => ({ value: c, label: n }))];
    default:
      return [any];
  }
}
const currentSubtypeOptions = computed(() => subtypeOptions(store.seriesKind));

function onKindChange() {
  store.seriesSubtype = ""; // reset — the old subtype rarely applies to a new kind
  store.fetchSeries(true);
}
function onSeriesChange() {
  store.fetchSeries(true);
}
function setCount(n: number) {
  store.seriesCount = n;
  store.fetchSeries(true);
}

// --- online-feed toggles (window mode) ---
const comets = computed({
  get: () => store.params.comets !== 0,
  set: (on: boolean) => store.fetch({ comets: on ? undefined : 0 }, true),
});
const satellites = computed({
  get: () => store.params.satellites !== 0,
  set: (on: boolean) => store.fetch({ satellites: on ? undefined : 0 }, true),
});

// --- window-mode client filters (no refetch) ---
const instrument = ref<"any" | "naked_eye" | "binocular" | "telescope">("any");
const notableOnly = ref(false);
const excludedKinds = ref<Set<string>>(new Set());
const selectedDay = ref<string | null>(null);

function dayInTz(ms: number): string {
  return fmtDateTime(ms, tz.value).slice(0, 10);
}
function toggleKind(k: string) {
  const s = new Set(excludedKinds.value);
  s.has(k) ? s.delete(k) : s.add(k);
  excludedKinds.value = s;
}

const kindsPresent = computed(() =>
  [...new Set(store.events.map((e) => e.kind))].sort(),
);

const filtered = computed<SkyEvent[]>(() =>
  store.events.filter((e) => {
    if (instrument.value !== "any" && e.visibility[instrument.value] <= 0)
      return false;
    if (notableOnly.value && !e.notable) return false;
    if (excludedKinds.value.has(e.kind)) return false;
    return true;
  }),
);

// The calendar grid always shows every event; focusing a day only narrows the LIST to that date.
const windowList = computed<SkyEvent[]>(() =>
  selectedDay.value
    ? filtered.value.filter((e) => dayInTz(e.peak_utc_ms) === selectedDay.value)
    : filtered.value,
);

// --- calendar month state (window mode) ---
const displayedMonth = ref(new Date());
function changeMonth(delta: number) {
  const d = displayedMonth.value;
  displayedMonth.value = new Date(d.getFullYear(), d.getMonth() + delta, 1);
}
function onSelectDay(day: string | null) {
  selectedDay.value = day;
  if (day) {
    const top = filtered.value.find((e) => dayInTz(e.peak_utc_ms) === day);
    if (top) store.select(top.id);
  }
}

const location = computed(() => store.query?.location);
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div>
        <div class="flex items-center gap-2">
          <h1 class="text-2xl font-semibold">{{ t("calendar.title") }}</h1>
          <HelpButton />
        </div>
        <p class="text-sm text-slate-400">{{ t("calendar.subtitle") }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <!-- Mode: date window vs next-N-by-type (centered sticky tab band) -->
        <Teleport to="#page-tabs">
          <TabBar
            :tabs="[
              { key: 'window', label: t('calendar.mode.byDate') },
              { key: 'series', label: t('calendar.mode.byType') },
            ]"
            :active="store.mode"
            @select="(k) => setMode(k as 'window' | 'series')"
          />
        </Teleport>

        <!-- Window mode: date ranges -->
        <div v-if="store.mode === 'window'" :class="segWrap">
          <button
            v-for="r in [30, 90, 180, 365]"
            :key="r"
            type="button"
            :class="[segBtn, rangeDays === r ? segActive : segIdle]"
            @click="setRange(r)"
          >
            {{ t("calendar.range.d" + r) }}
          </button>
        </div>

        <!-- Series mode: how many -->
        <div v-else :class="segWrap">
          <button
            v-for="n in [10, 20, 50, 100]"
            :key="n"
            type="button"
            :class="[segBtn, store.seriesCount === n ? segActive : segIdle]"
            @click="setCount(n)"
          >
            {{ n }}
          </button>
        </div>

        <span v-if="store.query" class="text-xs text-slate-500">
          {{ t("calendar.updated", { time: fmtClock(Date.now(), tz) }) }}
        </span>
        <button :class="btnGhost" @click="store.refresh()">
          {{ t("calendar.refresh") }}
        </button>
      </div>
    </div>

    <!-- Series picker: which type (+ subtype) -->
    <div
      v-if="store.mode === 'series'"
      :class="[card, 'flex flex-wrap items-end gap-4']"
    >
      <label class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("calendar.series.type") }}
        <div class="mt-1 flex items-center gap-1.5">
          <EventIcon :kind="store.seriesKind" class="h-4 w-4 shrink-0" />
          <select
            v-model="store.seriesKind"
            :class="input"
            @change="onKindChange"
          >
            <option v-for="k in seriesKinds" :key="k" :value="k">
              {{ t("calendar.kinds." + k) }}
            </option>
          </select>
        </div>
      </label>
      <label
        v-if="currentSubtypeOptions.length > 1"
        class="text-xs text-slate-500 dark:text-slate-400"
      >
        {{ t("calendar.series.subtype") }}
        <select
          v-model="store.seriesSubtype"
          :class="[input, 'mt-1']"
          @change="onSeriesChange"
        >
          <option
            v-for="o in currentSubtypeOptions"
            :key="o.value"
            :value="o.value"
          >
            {{ o.label }}
          </option>
        </select>
      </label>
      <span class="text-sm text-slate-400">
        {{
          t("calendar.series.heading", {
            count: store.seriesCount,
            type: t("calendar.kinds." + store.seriesKind),
          })
        }}
      </span>
    </div>

    <CollapsibleCard
      :title="t('calendar.sections.location')"
      storage-key="astrostack.events.section.location"
    >
      <LocationPicker
        v-if="location"
        :lat="location.lat"
        :lon="location.lon"
        @pick="(lat, lon) => store.fetch({ lat, lon }, true)"
      />
    </CollapsibleCard>

    <!-- Filters apply to the date-window list only -->
    <CollapsibleCard
      v-if="store.mode === 'window'"
      :title="t('calendar.sections.filters')"
      storage-key="astrostack.events.section.filters"
    >
      <div class="flex flex-wrap items-end gap-4">
        <label class="text-xs text-slate-500 dark:text-slate-400">
          {{ t("calendar.filters.instrument") }}
          <select v-model="instrument" :class="input">
            <option value="any">
              {{ t("calendar.filters.anyInstrument") }}
            </option>
            <option value="naked_eye">
              {{ t("calendar.tiers.naked_eye") }}
            </option>
            <option value="binocular">
              {{ t("calendar.tiers.binocular") }}
            </option>
            <option value="telescope">
              {{ t("calendar.tiers.telescope") }}
            </option>
          </select>
        </label>
        <label
          class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="notableOnly"
            type="checkbox"
            class="accent-brand-500"
          />
          {{ t("calendar.filters.notableOnly") }}
        </label>
        <label
          class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
        >
          <input v-model="comets" type="checkbox" class="accent-brand-500" />
          {{ t("calendar.filters.comets") }}
        </label>
        <label
          class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="satellites"
            type="checkbox"
            class="accent-brand-500"
          />
          {{ t("calendar.filters.satellites") }}
        </label>
      </div>
      <div v-if="kindsPresent.length" class="mt-3 flex flex-wrap gap-1.5">
        <button
          v-for="k in kindsPresent"
          :key="k"
          type="button"
          class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium transition-opacity"
          :class="[kindPillClass(k), excludedKinds.has(k) ? 'opacity-30' : '']"
          @click="toggleKind(k)"
        >
          <EventIcon :kind="k" class="h-3.5 w-3.5 shrink-0" />
          {{ t("calendar.kinds." + k) }}
        </button>
      </div>
    </CollapsibleCard>

    <p
      v-for="w in store.warnings"
      :key="w"
      class="text-xs text-amber-600 dark:text-amber-400"
    >
      ⚠ {{ w }}
    </p>

    <Spinner v-if="store.loading && !store.events.length">{{
      t("calendar.loading")
    }}</Spinner>
    <p v-if="store.error" class="text-sm text-danger-500">{{ store.error }}</p>

    <div class="grid gap-4 lg:grid-cols-2">
      <!-- Window: month calendar. Series: the next-N table (chronological). -->
      <div v-if="store.mode === 'window'" :class="card">
        <EventCalendar
          :events="filtered"
          :tz="tz"
          :month="displayedMonth"
          :selected-day="selectedDay"
          @change-month="changeMonth"
          @select-day="onSelectDay"
          @select-event="store.select"
        />
      </div>
      <EventsTable
        v-else
        :events="store.events"
        :tz="tz"
        :selected-id="store.selectedId"
        sort="date"
        max-height="40rem"
        @select="store.select"
      />

      <EventDetailPanel
        :event="store.selected"
        :tz="tz"
        :limits="store.query?.limits"
      />
    </div>

    <!-- Window mode only: focused-day banner + the ranked list -->
    <template v-if="store.mode === 'window'">
      <div
        v-if="selectedDay"
        class="flex items-center gap-2 text-sm text-slate-500 dark:text-slate-400"
      >
        <span>{{ t("calendar.day.showing", { day: selectedDay }) }}</span>
        <button
          class="rounded bg-slate-200 px-2 py-0.5 text-xs text-slate-700 hover:bg-slate-300 dark:bg-slate-700 dark:text-slate-200 dark:hover:bg-slate-600"
          @click="onSelectDay(null)"
        >
          {{ t("calendar.day.showAll") }}
        </button>
      </div>

      <EventsTable
        :events="windowList"
        :tz="tz"
        :selected-id="store.selectedId"
        sort="score"
        max-height="40rem"
        @select="store.select"
      />
    </template>
  </div>
</template>
