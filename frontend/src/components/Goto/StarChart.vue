<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import { useSkyCatalog, type SkyMapData } from "@/composables/useSkyCatalog";
import {
  useSkyMapCanvas,
  useMilkyWayToggle,
  type SkyBodyView,
  type SkyTargetView,
} from "@/composables/useSkyMapCanvas";
import { precessFromJ2000, equatorialToHorizontal } from "@/utils/astro";
import { tzForLocation, fmtClock } from "@/utils/tz";
import { btnGhost, btnPrimary } from "@/constants/styles";

// Interactive alt/az sky map centred on the star to center now: real constellation figures + names,
// Moon/planets, a faint procedural Milky Way, drag-to-pan and wheel/pinch zoom, and a ±12 h time
// slider. The star catalogue is a lazy-loaded static asset; the canvas redraws only on interaction,
// so it's idle-cheap. `fill` stretches the canvas to the parent (the fullscreen modal instance).
defineProps<{ fill?: boolean }>();
const emit = defineEmits<{ (e: "expand"): void }>();

const { t } = useI18n();
const store = useGotoStore();
const { data, loading, load } = useSkyCatalog();

const canvas = ref<HTMLCanvasElement | null>(null);

const observer = () => {
  const q = store.query;
  if (!q) return null;
  return {
    lat: q.location.lat,
    lon: q.location.lon,
    atMs: q.at_utc_ms + store.timeOffsetMs,
  };
};

// While the time slider is scrubbed the backend alt/az is stale — reproject the recommended star
// client-side from the catalogue (looked up by name); when it isn't there, hide the ring honestly.
let nameIndex: Map<string, number> | null = null;
function catalogIndex(d: SkyMapData, name: string): number {
  if (!nameIndex)
    nameIndex = new Map(d.names.map(([i, n]) => [n.toLowerCase(), i]));
  return nameIndex.get(name.toLowerCase()) ?? -1;
}
function scrubbedTarget(): SkyTargetView | null {
  const r = store.recommended;
  const d = data.value;
  const q = store.query;
  if (!r || !d || !q) return null;
  const idx = catalogIndex(d, r.name);
  if (idx < 0) return null;
  const atMs = q.at_utc_ms + store.timeOffsetMs;
  const p = precessFromJ2000(d.stars[idx][0], d.stars[idx][1], atMs);
  const h = equatorialToHorizontal(
    p.ra,
    p.dec,
    q.location.lat,
    q.location.lon,
    atMs,
  );
  return { alt: h.alt, az: h.az, label: r.hc_name || r.name };
}
const target = (): SkyTargetView | null => {
  const r = store.recommended;
  if (!r) return null;
  if (store.timeOffsetMs !== 0) return scrubbedTarget();
  return { alt: r.alt_deg, az: r.az_deg, label: r.hc_name || r.name };
};

const bodies = (): SkyBodyView[] =>
  store.bodies.map((b) => ({
    name: b.name,
    kind: b.kind,
    alt: b.alt_deg,
    az: b.az_deg,
    mag: b.mag,
    phase: b.phase,
  }));
const bodyLabel = (name: string) => t(`goto.sky.bodies.${name}`);

const { fovDeg, zoomBy, resetToTarget, wholeSky } = useSkyMapCanvas({
  canvas,
  data,
  observer,
  target,
  bodies,
  bodyLabel,
  bodiesAlpha: () => (store.timeOffsetMs !== 0 ? 0.25 : 1),
  cardinalLabel: (code) => t(`goto.compass.${code}`),
});
const { milkyWayOn, toggle: toggleMilkyWay } = useMilkyWayToggle();

const hasTarget = computed(() => !!store.recommended);
const scrubbed = computed(() => store.timeOffsetMs !== 0);

// Time slider: ±12 h in 5-min steps; input events are rAF-coalesced so a fast scrub costs at most
// one recompute+redraw per frame.
const offsetMin = computed(() => Math.round(store.timeOffsetMs / 60_000));
let pendingMin = 0;
let sliderRaf = 0;
function onSliderInput(e: Event) {
  pendingMin = Number((e.target as HTMLInputElement).value);
  if (sliderRaf) return;
  sliderRaf = requestAnimationFrame(() => {
    sliderRaf = 0;
    store.timeOffsetMs = pendingMin * 60_000;
  });
}
function resetOffset() {
  store.timeOffsetMs = 0;
}

// Signed offset + the effective wall-clock at the observing site (the page's location timezone).
const tz = computed(() => {
  const l = store.query?.location;
  return l
    ? tzForLocation(l.lat, l.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone;
});
const offsetLabel = computed(() => {
  const q = store.query;
  if (!q) return "";
  const min = offsetMin.value;
  const sign = min < 0 ? "−" : "+";
  const abs = Math.abs(min);
  const clock = fmtClock(q.at_utc_ms + store.timeOffsetMs, tz.value);
  return `${sign}${Math.floor(abs / 60)}h${String(abs % 60).padStart(2, "0")} · ${clock}`;
});

onMounted(() => {
  void load(); // pull the star/constellation dataset (lazy chunk) on first mount
});
onBeforeUnmount(() => {
  if (sliderRaf) cancelAnimationFrame(sliderRaf);
});
</script>

<template>
  <div :class="fill ? 'flex h-full min-h-0 flex-col' : ''">
    <div
      class="relative overflow-hidden rounded-md border border-slate-800 bg-slate-950"
      :class="fill ? 'min-h-0 flex-1' : ''"
    >
      <canvas
        ref="canvas"
        class="block w-full touch-none select-none"
        :class="[
          fill ? 'h-full' : 'h-72',
          hasTarget ? 'cursor-grab active:cursor-grabbing' : '',
        ]"
      />

      <!-- Loading the star catalogue (first mount only). -->
      <div
        v-if="loading && !data"
        class="absolute inset-0 flex items-center justify-center text-xs text-slate-400"
      >
        {{ t("goto.sky.loading") }}
      </div>

      <!-- Sequence complete → nothing to point at. -->
      <div
        v-else-if="!hasTarget"
        class="absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-slate-400"
      >
        {{ t("goto.sky.empty") }}
      </div>

      <!-- Zoom / recentre / layer controls. -->
      <div v-if="hasTarget" class="absolute right-2 top-2 flex flex-col gap-1">
        <button
          :class="[btnGhost, 'h-7 w-7 !p-0 text-base leading-none']"
          :title="t('goto.sky.controls.zoomIn')"
          @click="zoomBy(1 / 1.4)"
        >
          +
        </button>
        <button
          :class="[btnGhost, 'h-7 w-7 !p-0 text-base leading-none']"
          :title="t('goto.sky.controls.zoomOut')"
          @click="zoomBy(1.4)"
        >
          −
        </button>
        <button
          :class="[btnGhost, 'h-7 w-7 !p-0 text-xs leading-none']"
          :title="t('goto.sky.controls.reset')"
          @click="resetToTarget()"
        >
          ⌖
        </button>
        <button
          :class="[btnGhost, 'h-7 w-7 !p-0 text-xs leading-none']"
          :title="t('goto.sky.controls.wholeSky')"
          @click="wholeSky()"
        >
          🜨
        </button>
        <button
          :class="[
            milkyWayOn ? btnPrimary : btnGhost,
            'h-7 w-7 !p-0 text-xs leading-none',
          ]"
          :title="t('goto.sky.milkyWay')"
          :aria-pressed="milkyWayOn"
          @click="toggleMilkyWay()"
        >
          ≋
        </button>
        <button
          v-if="!fill"
          :class="[btnGhost, 'h-7 w-7 !p-0 text-xs leading-none']"
          :title="t('goto.sky.expand')"
          @click="emit('expand')"
        >
          ⛶
        </button>
      </div>

      <!-- Field-of-view readout. -->
      <span
        v-if="hasTarget"
        class="absolute bottom-2 left-2 text-[10px] tabular-nums text-slate-500"
      >
        {{ Math.round(fovDeg) }}°
      </span>
    </div>

    <!-- Time slider: scrub ±12 h around the page time to see how the sky moves (fully client-side). -->
    <div v-if="hasTarget" class="mt-2 flex items-center gap-2">
      <button
        :class="[btnGhost, 'h-6 w-6 shrink-0 !p-0 text-xs leading-none']"
        :title="t('goto.sky.now')"
        :disabled="!scrubbed"
        @click="resetOffset"
      >
        ⟲
      </button>
      <input
        type="range"
        min="-720"
        max="720"
        step="5"
        :value="offsetMin"
        :aria-label="t('goto.sky.offset')"
        class="min-w-0 grow accent-brand-500"
        @input="onSliderInput"
      />
      <span
        class="w-32 shrink-0 text-right text-xs tabular-nums text-slate-400"
      >
        {{ offsetLabel }}
      </span>
    </div>
    <p
      v-if="scrubbed"
      class="mt-1 text-center text-[11px] text-amber-600 dark:text-amber-400"
    >
      {{ t("goto.sky.scrubHint") }}
    </p>

    <!-- The actionable naked-eye instruction + interaction hint. -->
    <p
      v-if="store.recommended && !scrubbed"
      class="mt-2 text-center text-xs text-slate-500 dark:text-slate-400"
    >
      {{
        t("goto.sky.face", {
          dir: t(`goto.compass.${store.recommended.compass}`),
          alt: Math.round(store.recommended.alt_deg),
        })
      }}
      <span class="text-slate-600">· {{ t("goto.sky.hint") }}</span>
    </p>
  </div>
</template>
