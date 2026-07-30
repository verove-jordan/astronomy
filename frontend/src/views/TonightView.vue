<script setup lang="ts">
import { onMounted, computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { MAG_ALL, useSkyStore } from "@/stores/sky";
import { useAutoRefresh } from "@/composables/useAutoRefresh";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import Pill from "@/components/Common/Pill.vue";
import Spinner from "@/components/Common/Spinner.vue";
import ScoreBadge from "@/components/Common/ScoreBadge.vue";
import SkyControlBar from "@/components/Sky/SkyControlBar.vue";
import DarkSkyFinder from "@/components/Sky/DarkSkyFinder.vue";
import TargetDetailPanel from "@/components/Sky/TargetDetailPanel.vue";
import SkyMap from "@/components/Dataviz/SkyMap.vue";
import AltitudeChart from "@/components/Dataviz/AltitudeChart.vue";
import AstroWeatherPanel from "@/components/Sky/AstroWeatherPanel.vue";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import TwoPane from "@/components/Common/TwoPane.vue";
import TabBar from "@/components/Common/TabBar.vue";
import IconStar from "@/components/Icons/IconStar.vue";
import { card, input, btnGhost, skyTypePillClass } from "@/constants/styles";
import { scrollElementToTop } from "@/utils/scroll";
import {
  tzForLocation,
  fmtClock,
  fmtDateTime,
  zonedWallToISO,
  nowInZone,
} from "@/utils/tz";
import type { SkyTarget } from "@/types";

const { t } = useI18n();
const store = useSkyStore();

// In-page tabs: the deep-sky planner vs the dark-sky finder. Persisted across reloads. (The
// polar-alignment aid moved to the GoTo alignment page; a stale saved "polar" falls back here.)
const TAB_KEY = "astrostack.tonight.tab";
const savedTab = localStorage.getItem(TAB_KEY);
const tab = ref<"targets" | "darksky">(
  savedTab === "darksky" ? savedTab : "targets",
);
watch(tab, (v) => localStorage.setItem(TAB_KEY, v));

onMounted(() => store.fetch());

// "Use as my location" from the dark-sky finder routes through the control bar's setLatLon (which
// fills the form, moves the map marker, and re-scores), then switches back to the targets tab.
const controlBar = ref<InstanceType<typeof SkyControlBar> | null>(null);
function useDarkSite(lat: number, lon: number) {
  controlBar.value?.setLatLon(lat, lon);
  tab.value = "targets";
}
const { enabled: autoRefresh } = useAutoRefresh(() => store.refresh(), 90_000);

// Clicking a target in the table selects it and reveals its visibility chart: scroll the chart card
// up so it sits just below the sticky header (topbar + page-tabs band), bringing chart + preview
// into focus. Waits a tick so the chart has re-rendered for the newly selected target.
const chartCard = ref<HTMLElement | null>(null);
function stickyOffset(): number {
  const tabs = document.getElementById("page-tabs");
  return (tabs ? tabs.getBoundingClientRect().bottom : 0) + 8;
}
async function selectTarget(name: string) {
  store.select(name);
  await nextTick();
  if (chartCard.value) scrollElementToTop(chartCard.value, stickyOffset());
}

// All "tonight" times display in the SELECTED LOCATION's timezone (from its coordinates), not the browser's.
const tz = computed(() => {
  const l = store.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});

// Plan for "now" (real-time) or a specific date/time entered in the observer's local time.
const useCustomTime = ref(false);
const customLocal = ref("");

function applyTime() {
  if (useCustomTime.value && customLocal.value) {
    store.fetch({ at: zonedWallToISO(customLocal.value, tz.value) }, true);
  }
}
watch(useCustomTime, (on) => {
  if (on) {
    autoRefresh.value = false;
    customLocal.value = nowInZone(
      store.query?.at_utc_ms ?? Date.now(),
      tz.value,
    );
    applyTime();
  } else {
    store.fetch({ at: undefined }, true); // back to real-time
  }
});
watch(customLocal, () => applyTime());
watch(tz, () => {
  if (useCustomTime.value) applyTime();
});

// Client-side view filters (no refetch) — stack on top of the table's per-column search.
const typeFilter = ref("");
const minScore = ref(0);
const fitsFrame = ref(false);
const fullyDark = ref(false);
const awayMoon = ref(false);
const favoritesOnly = ref(false);
const narrowbandOnly = ref(false);

// Limiting magnitude — a SERVER-side query param, unlike the view filters above: the returned list
// is score-truncated, so only the backend gate can actually reveal fainter objects (galaxies).
// Dragging updates the label live; the refetch fires on release (@change). Persisted with the sky
// query; the map follows automatically since it plots store.targets.
const magLimit = ref(store.params.max_mag ?? MAG_ALL);
function applyMagLimit() {
  void store.fetch({ max_mag: magLimit.value });
}

const availableTypes = computed(() =>
  [...new Set(store.targets.map((tg) => tg.type))].sort(),
);

const filtered = computed<SkyTarget[]>(() => {
  const out = store.targets.filter((tg) => {
    if (typeFilter.value && tg.type !== typeFilter.value) return false;
    if (tg.score < minScore.value) return false;
    if (fitsFrame.value && !(tg.fov_fill_pct > 0 && tg.fov_fill_pct <= 100))
      return false;
    if (fullyDark.value && !(tg.dark_hours_above_min > 0)) return false;
    if (awayMoon.value && tg.moon_sep_deg < 30) return false;
    if (favoritesOnly.value && !store.isFavorite(tg.name)) return false;
    if (narrowbandOnly.value && tg.composition.broadband) return false;
    return true;
  });
  return [...out].sort((a, b) => b.score - a.score);
});

type Row = Record<string, unknown>;
const rows = computed<Row[]>(() =>
  filtered.value.map((tg) => ({
    name: tg.name,
    common_name: tg.common_name ?? "",
    fav: store.isFavorite(tg.name) ? 0 : 1, // 0 sorts favorites to the top
    type: tg.type,
    palette: tg.composition.palette,
    score: tg.score,
    score_live: tg.score_live,
    alt_now_deg: tg.alt_now_deg,
    max_alt_deg: tg.max_alt_deg,
    transit_utc_ms: tg.transit_utc_ms,
    transit_local: tg.transit_local,
    size_arcmin: tg.size_arcmin,
    size_minor_arcmin: tg.size_minor_arcmin ?? 0,
    mag_v: tg.mag_v,
    fov_fill_pct: tg.fov_fill_pct,
    chosen_eyepiece: tg.chosen_eyepiece ?? "",
    mag_x: tg.mag_x ?? 0,
    true_fov_deg: tg.true_fov_deg ?? 0,
    moon_sep_deg: tg.moon_sep_deg,
    visible: tg.flags.visible,
  })),
);

const deg = (v: unknown): string => {
  const n = Number(v);
  return Number.isFinite(n) ? `${Math.round(n)}°` : "—";
};
// Size as the true ellipse "major'×minor'" when OpenNGC supplies a minor axis, else just the major axis.
const sizeFmt = (v: unknown, row: Row): string => {
  const maj = Number(v);
  if (!(maj > 0)) return "—";
  const min = Number(row.size_minor_arcmin);
  return min > 0
    ? `${maj.toFixed(1)}'×${min.toFixed(1)}'`
    : `${maj.toFixed(1)}'`;
};
const magFmt = (v: unknown): string => {
  const n = Number(v);
  return n ? n.toFixed(1) : "—";
};
const pctFmt = (v: unknown): string => {
  const n = Number(v);
  return n > 0 ? `${Math.round(n)}%` : "—";
};
const magXFmt = (v: unknown): string => {
  const n = Number(v);
  return n > 0 ? `${Math.round(n)}×` : "—";
};
const fovDegFmt = (v: unknown): string => {
  const n = Number(v);
  return n > 0 ? `${n.toFixed(2)}°` : "—";
};
const epFmt = (v: unknown): string => (v ? String(v) : "—");

// In visual (eyepiece) mode the table swaps "Frame fit" for the recommended eyepiece + its view.
const visualMode = computed(() => store.query?.equipment.mode === "visual");

const columns = computed<Column<Row>[]>(() => [
  { key: "fav", label: "★", sortable: true, align: "center" },
  {
    key: "name",
    label: t("tonight.cols.name"),
    sortable: true,
    searchable: true,
    // Display is the #cell-name slot; this format only feeds search so a common name ("Fireworks
    // Galaxy") matches the query too.
    format: (v, row) =>
      row.common_name ? `${String(v)} ${String(row.common_name)}` : String(v),
  },
  {
    key: "type",
    label: t("tonight.cols.type"),
    sortable: true,
    searchable: true,
    format: (v) => t("tonight.types." + String(v)),
  },
  {
    key: "palette",
    label: t("tonight.cols.palette"),
    sortable: true,
    searchable: true,
  },
  {
    key: "score",
    label: t("tonight.cols.score"),
    sortable: true,
    align: "right",
  },
  {
    key: "score_live",
    label: t("tonight.cols.scoreLive"),
    sortable: true,
    align: "right",
  },
  {
    key: "alt_now_deg",
    label: t("tonight.cols.altNow"),
    sortable: true,
    align: "right",
    format: deg,
  },
  {
    key: "max_alt_deg",
    label: t("tonight.cols.maxAlt"),
    sortable: true,
    align: "right",
    format: deg,
  },
  {
    key: "transit_utc_ms",
    label: t("tonight.cols.transit"),
    sortable: true,
    align: "right",
    format: (v) => fmtClock(Number(v), tz.value),
  },
  {
    key: "size_arcmin",
    label: t("tonight.cols.size"),
    sortable: true,
    align: "right",
    format: sizeFmt,
  },
  {
    key: "mag_v",
    label: t("tonight.cols.mag"),
    sortable: true,
    align: "right",
    format: magFmt,
  },
  ...(visualMode.value
    ? ([
        {
          key: "chosen_eyepiece",
          label: t("tonight.cols.bestEp"),
          sortable: true,
          searchable: true,
          format: epFmt,
        },
        {
          key: "mag_x",
          label: t("tonight.cols.power"),
          sortable: true,
          align: "right",
          format: magXFmt,
        },
        {
          key: "true_fov_deg",
          label: t("tonight.cols.trueField"),
          sortable: true,
          align: "right",
          format: fovDegFmt,
        },
      ] as Column<Row>[])
    : ([
        {
          key: "fov_fill_pct",
          label: t("tonight.cols.fovFit"),
          sortable: true,
          align: "right",
          format: pctFmt,
        },
      ] as Column<Row>[])),
  {
    key: "moon_sep_deg",
    label: t("tonight.cols.moonSep"),
    sortable: true,
    align: "right",
    format: deg,
  },
]);

function rowClass(row: Row): string {
  const selected = row.name === store.selectedName;
  const base = selected
    ? "cursor-pointer bg-brand-50 dark:bg-brand-900/30"
    : "cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800/50";
  return row.visible ? base : `${base} opacity-50`;
}

const nowMs = computed(() => store.query?.at_utc_ms ?? 0);
const moonPhaseLabel = computed(() => {
  const m = store.darkWindow?.moon;
  return m ? t("tonight.moonPhase." + m.phase) : "";
});

const minAlt = computed(() => store.query?.min_alt_deg ?? 30);
const fovW = computed(() => store.query?.equipment.fov_w_deg ?? 1);
const fovH = computed(() => store.query?.equipment.fov_h_deg ?? 1);
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div>
        <h1 class="text-2xl font-semibold">{{ t("tonight.title") }}</h1>
        <p class="text-sm text-slate-400">{{ t("tonight.subtitle") }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <Teleport to="#page-tabs">
          <TabBar
            :tabs="[
              { key: 'targets', label: t('tonight.tabs.targets') },
              { key: 'darksky', label: t('tonight.tabs.darksky') },
            ]"
            :active="tab"
            @select="(k) => (tab = k as 'targets' | 'darksky')"
          />
        </Teleport>
        <template v-if="tab === 'targets'">
          <label class="flex items-center gap-1.5 text-sm text-slate-400">
            <input
              v-model="useCustomTime"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.time.specific") }}
          </label>
          <input
            v-if="useCustomTime"
            v-model="customLocal"
            type="datetime-local"
            :class="[input, 'w-auto text-sm']"
          />
          <label
            v-if="!useCustomTime"
            class="flex items-center gap-1.5 text-sm text-slate-400"
          >
            <input
              v-model="autoRefresh"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.autoRefresh") }}
          </label>
          <span v-if="store.query" class="text-xs text-slate-500">
            {{
              t("tonight.updated", {
                time: fmtClock(store.query.at_utc_ms, tz),
              })
            }}
          </span>
          <button :class="btnGhost" @click="store.refresh()">
            {{ t("tonight.refresh") }}
          </button>
        </template>
      </div>
    </div>

    <section v-show="tab === 'targets'" class="space-y-4">
      <CollapsibleCard
        :title="t('tonight.sections.setup')"
        storage-key="astrostack.sky.section.setup"
      >
        <SkyControlBar ref="controlBar" />
      </CollapsibleCard>

      <!-- Darkness summary -->
      <div
        v-if="store.darkWindow"
        :class="[card, 'flex flex-wrap items-center gap-x-6 gap-y-1 text-sm']"
      >
        <span class="text-slate-600 dark:text-slate-300">
          🌙
          {{
            t("tonight.dark.window", {
              dusk: fmtDateTime(store.darkWindow.dusk_utc_ms, tz),
              dawn: fmtDateTime(store.darkWindow.dawn_utc_ms, tz),
            })
          }}
        </span>
        <span class="text-slate-500 dark:text-slate-400">
          {{
            t("tonight.dark.duration", { hours: store.darkWindow.dark_hours })
          }}
        </span>
        <span class="text-slate-500 dark:text-slate-400">
          {{ t("tonight.dark.moon") }}: {{ moonPhaseLabel }} ·
          {{
            t("tonight.dark.moonIllum", {
              pct: Math.round(store.darkWindow.moon.illum_fraction * 100),
            })
          }}
          ({{
            store.darkWindow.moon.up_now
              ? t("tonight.dark.moonUp")
              : t("tonight.dark.moonDown")
          }})
        </span>
        <span
          v-if="store.darkWindow.no_astro_dark"
          class="text-amber-600 dark:text-amber-400"
        >
          {{ t("tonight.dark.noDark") }}
        </span>
      </div>

      <!-- View filters -->
      <CollapsibleCard
        :title="t('tonight.sections.filters')"
        storage-key="astrostack.sky.section.filters"
      >
        <div class="flex flex-wrap items-end gap-4">
          <label class="text-xs text-slate-500 dark:text-slate-400">
            {{ t("tonight.controls.type") }}
            <select v-model="typeFilter" :class="input">
              <option value="">{{ t("tonight.controls.allTypes") }}</option>
              <option v-for="ty in availableTypes" :key="ty" :value="ty">
                {{ t("tonight.types." + ty) }}
              </option>
            </select>
          </label>
          <label class="text-xs text-slate-500 dark:text-slate-400">
            {{ t("tonight.controls.minScore") }}: {{ minScore }}
            <input
              v-model.number="minScore"
              type="range"
              min="0"
              max="100"
              class="block w-40 accent-brand-500"
            />
          </label>
          <label class="text-xs text-slate-500 dark:text-slate-400">
            {{ t("tonight.controls.magLimit") }}:
            {{
              magLimit >= MAG_ALL
                ? t("tonight.controls.magAll")
                : magLimit.toFixed(1)
            }}
            <input
              v-model.number="magLimit"
              type="range"
              min="4"
              :max="MAG_ALL"
              step="0.5"
              class="block w-40 accent-brand-500"
              data-demo="tonight-mag"
              @change="applyMagLimit"
            />
          </label>
          <label
            class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
          >
            <input
              v-model="fitsFrame"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.controls.fitsFrame") }}
          </label>
          <label
            class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
          >
            <input
              v-model="fullyDark"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.controls.fullyDark") }}
          </label>
          <label
            class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
          >
            <input
              v-model="awayMoon"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.controls.excludeMoon") }}
          </label>
          <label
            class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
          >
            <input
              v-model="favoritesOnly"
              type="checkbox"
              class="accent-amber-400"
            />
            {{ t("tonight.controls.favoritesOnly") }}
          </label>
          <label
            class="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400"
          >
            <input
              v-model="narrowbandOnly"
              type="checkbox"
              class="accent-brand-500"
            />
            {{ t("tonight.controls.narrowbandOnly") }}
          </label>
        </div>
      </CollapsibleCard>

      <Spinner v-if="store.loading && !store.targets.length">{{
        t("tonight.loading")
      }}</Spinner>
      <p v-if="store.error" class="text-sm text-danger-500">
        {{ store.error }}
      </p>

      <GenericTable
        :columns="columns"
        :rows="rows"
        :row-class="rowClass"
        max-height="30rem"
      >
        <template #cell-fav="{ row }">
          <button
            type="button"
            :class="
              store.isFavorite(String(row.name))
                ? 'text-amber-400'
                : 'text-slate-400 hover:text-amber-400'
            "
            :aria-label="
              store.isFavorite(String(row.name))
                ? t('tonight.fav.remove')
                : t('tonight.fav.add')
            "
            @click="store.toggleFavorite(String(row.name))"
          >
            <IconStar :filled="store.isFavorite(String(row.name))" />
          </button>
        </template>
        <template #cell-name="{ row }">
          <button
            class="text-left"
            :aria-pressed="row.name === store.selectedName"
            @click="selectTarget(String(row.name))"
          >
            <span
              class="font-medium text-brand-600 hover:underline dark:text-brand-300"
              >{{ row.name }}</span
            >
            <span
              v-if="row.common_name"
              class="block text-xs text-slate-400 dark:text-slate-500"
              >{{ row.common_name }}</span
            >
          </button>
        </template>
        <template #cell-type="{ row }">
          <Pill :color-class="skyTypePillClass(String(row.type))">{{
            t("tonight.types." + row.type)
          }}</Pill>
        </template>
        <template #cell-score="{ row }">
          <ScoreBadge :score="Number(row.score)" />
        </template>
        <template #cell-score_live="{ row }">
          <ScoreBadge
            v-if="row.score_live != null"
            :score="Number(row.score_live)"
          />
          <span
            v-else
            class="text-slate-500"
            :title="t('tonight.weather.noForecastNight')"
            >—</span
          >
        </template>
      </GenericTable>

      <!-- Night chart (Sun + Moon + the selected object's altitude) beside tonight's conditions. -->
      <TwoPane split="even">
        <template #main>
          <div v-if="store.darkWindow" ref="chartCard" :class="card">
            <h3
              class="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("tonight.chart.title") }}
            </h3>
            <AltitudeChart
              :target="store.selected"
              :dark-window="store.darkWindow"
              :min-alt-deg="minAlt"
              :now-ms="nowMs"
              :tz="tz"
            />
          </div>
        </template>
        <template #aside>
          <AstroWeatherPanel />
        </template>
      </TwoPane>

      <div class="grid gap-4 lg:grid-cols-2">
        <TargetDetailPanel
          :target="store.selected"
          :fov-w-deg="fovW"
          :fov-h-deg="fovH"
        />
        <CollapsibleCard
          :title="t('tonight.sections.map')"
          storage-key="astrostack.sky.section.map"
        >
          <SkyMap />
          <p class="mt-1 text-xs text-slate-400">{{ t("tonight.map.hint") }}</p>
        </CollapsibleCard>
      </div>
    </section>

    <section v-if="tab === 'darksky'" class="space-y-4">
      <DarkSkyFinder @use-location="useDarkSite" />
    </section>
  </div>
</template>
