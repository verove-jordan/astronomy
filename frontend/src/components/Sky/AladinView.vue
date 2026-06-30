<script setup lang="ts">
import { ref, computed, onMounted, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { SkyTarget } from "@/types";

// Aladin Lite v3 sky viewer, loaded on demand from the CDS CDN (it needs internet for survey tiles
// anyway). Shows the selected object centered, with the camera's field-of-view rectangle drawn on the
// real sky so you can judge framing. Falls back to the external links if it can't load.
const props = defineProps<{
  target: SkyTarget | null;
  fovWDeg: number;
  fovHDeg: number;
}>();
const { t } = useI18n();

const el = ref<HTMLDivElement | null>(null);
const failed = ref(false);

const ALADIN_SRC =
  "https://aladin.cds.unistra.fr/AladinLite/api/v3/latest/aladin.js";

/* eslint-disable @typescript-eslint/no-explicit-any */
let aladin: any = null;
let overlay: any = null;
let A: any = null;
let initialized = false;

function loadAladin(): Promise<any> {
  const w = window as any;
  if (w.A?.init) return w.A.init.then(() => w.A);
  return new Promise((resolve, reject) => {
    let s = document.querySelector<HTMLScriptElement>(
      `script[src="${ALADIN_SRC}"]`,
    );
    if (!s) {
      s = document.createElement("script");
      s.src = ALADIN_SRC;
      s.async = true;
      s.onerror = () => reject(new Error("aladin script failed to load"));
      document.head.appendChild(s);
    }
    const ready = () => {
      const api = (window as any).A;
      if (api?.init) api.init.then(() => resolve(api)).catch(reject);
      else window.setTimeout(ready, 150);
    };
    s.addEventListener("load", ready);
    ready();
  });
}

// Four corners of the camera field of view (degrees), RA scaled by cos(dec) so the box stays square.
function fovCorners(ra: number, dec: number): [number, number][] {
  const hw =
    props.fovWDeg / 2 / Math.max(Math.cos((dec * Math.PI) / 180), 1e-3);
  const hh = props.fovHDeg / 2;
  return [
    [ra - hw, dec - hh],
    [ra + hw, dec - hh],
    [ra + hw, dec + hh],
    [ra - hw, dec + hh],
  ];
}

function showTarget() {
  if (!aladin || !A || !props.target) return;
  const { ra_deg: ra, dec_deg: dec } = props.target;
  aladin.gotoRaDec(ra, dec);
  aladin.setFoV(Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1);
  overlay.removeAll();
  overlay.add(A.polygon(fovCorners(ra, dec)));
}

async function ensureInit() {
  if (initialized || !el.value || !props.target) return;
  initialized = true;
  try {
    A = await loadAladin();
    aladin = A.aladin(el.value, {
      survey: "P/DSS2/color",
      cooFrame: "ICRSd",
      fov: Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1,
      showReticle: false,
      showLayersControl: false,
      showFullscreenControl: false,
      showGotoControl: false,
      showSimbadPointerControl: false,
    });
    overlay = A.graphicOverlay({ color: "#6366f1", lineWidth: 2 });
    aladin.addOverlay(overlay);
    showTarget();
  } catch {
    failed.value = true;
    initialized = false;
  }
}

onMounted(ensureInit);
// Redraw on a new target AND when the field of view changes (e.g. a Barlow or optics edit), so the
// framing rectangle and zoom always reflect the current setup.
watch(
  () => [props.target?.name, props.fovWDeg, props.fovHDeg],
  () => {
    if (!initialized) void ensureInit();
    else showTarget();
  },
);
/* eslint-enable @typescript-eslint/no-explicit-any */

const aladinUrl = computed(() => {
  const tg = props.target;
  if (!tg) return "#";
  const fov = (Math.max(props.fovWDeg, props.fovHDeg) * 3 || 1).toFixed(2);
  return `https://aladin.cds.unistra.fr/AladinLite/?target=${tg.ra_deg}%20${tg.dec_deg}&fov=${fov}`;
});
const googleUrl = computed(
  () =>
    `https://www.google.com/search?tbm=isch&q=${encodeURIComponent(
      props.target?.name ?? "",
    )}`,
);
</script>

<template>
  <div>
    <div
      ref="el"
      class="h-64 w-full overflow-hidden rounded-md border border-slate-200 bg-black dark:border-slate-700"
    />
    <p v-if="failed" class="mt-1 text-xs text-slate-400">
      {{ t("tonight.preview.unavailable") }}
    </p>
    <div class="mt-1 flex gap-4 text-xs">
      <a
        :href="aladinUrl"
        target="_blank"
        rel="noopener"
        class="text-brand-600 hover:underline dark:text-brand-300"
        >{{ t("tonight.preview.aladin") }}</a
      >
      <a
        :href="googleUrl"
        target="_blank"
        rel="noopener"
        class="text-brand-600 hover:underline dark:text-brand-300"
        >{{ t("tonight.preview.google") }}</a
      >
    </div>
  </div>
</template>
