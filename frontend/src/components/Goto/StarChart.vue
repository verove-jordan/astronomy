<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import { useSkyCatalog } from "@/composables/useSkyCatalog";
import {
  useSkyMapCanvas,
  type SkyBodyView,
} from "@/composables/useSkyMapCanvas";
import { btnGhost } from "@/constants/styles";

// Interactive alt/az sky map centred on the star to center now: real constellation figures + names,
// Moon/planets, drag-to-pan and wheel/pinch zoom. The star catalogue is a lazy-loaded static asset; the
// canvas redraws only on interaction, so it's idle-cheap.
const { t } = useI18n();
const store = useGotoStore();
const { data, loading, load } = useSkyCatalog();

const canvas = ref<HTMLCanvasElement | null>(null);

const observer = () => {
  const q = store.query;
  if (!q) return null;
  return { lat: q.location.lat, lon: q.location.lon, atMs: q.at_utc_ms };
};
const target = () => {
  const r = store.recommended;
  if (!r) return null;
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
});

const hasTarget = computed(() => !!store.recommended);

onMounted(() => {
  void load(); // pull the star/constellation dataset (lazy chunk) on first mount
});
</script>

<template>
  <div>
    <div
      class="relative overflow-hidden rounded-md border border-slate-800 bg-slate-950"
    >
      <canvas
        ref="canvas"
        class="block h-72 w-full touch-none select-none"
        :class="hasTarget ? 'cursor-grab active:cursor-grabbing' : ''"
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

      <!-- Zoom / recentre controls. -->
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
      </div>

      <!-- Field-of-view readout. -->
      <span
        v-if="hasTarget"
        class="absolute bottom-2 left-2 text-[10px] tabular-nums text-slate-500"
      >
        {{ Math.round(fovDeg) }}°
      </span>
    </div>

    <!-- The actionable naked-eye instruction + interaction hint. -->
    <p
      v-if="store.recommended"
      class="mt-2 text-center text-xs text-slate-500 dark:text-slate-400"
    >
      {{
        t("goto.sky.face", {
          dir: store.recommended.compass,
          alt: Math.round(store.recommended.alt_deg),
        })
      }}
      <span class="text-slate-600">· {{ t("goto.sky.hint") }}</span>
    </p>
  </div>
</template>
