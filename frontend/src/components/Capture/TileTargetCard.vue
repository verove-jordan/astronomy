<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { btnPrimary } from "@/constants/styles";
import { apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";
import { decToDMS, raToHMS } from "@/utils/sexagesimal";
import type { MosaicTile } from "@/types";

// Arriving here from a mosaic tile: what is being shot, and getting the telescope pointed at it.
//
// The slew starts on its own (that is the point of the button in the tile table), but three things
// are non-negotiable around hardware that can swing a tube into a tripod leg: the target is spelled
// out before anything moves, STOP is always on screen, and a refusal — below the horizon, mount not
// aligned — is shown as text rather than swallowed.
const { t } = useI18n();
const store = useCaptureStore();

const props = defineProps<{
  tile: MosaicTile | null;
  objectName?: string;
  autoSlew?: boolean;
}>();

const error = ref("");
const slewing = ref(false);
const done = ref(false);
const centering = ref(false);
const centerResult = ref<{
  centered: boolean;
  final_arcsec: number;
  attempts: { iteration: number; error_arcsec: number }[];
} | null>(null);

const mount = computed(() => store.mount?.mount ?? null);
const connected = computed(() => !!store.connected.mount);

async function slew() {
  if (!props.tile) return;
  error.value = "";
  slewing.value = true;
  try {
    await store.slew(props.tile.ra_deg, props.tile.dec_deg);
    done.value = true;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    slewing.value = false;
  }
}

// Centring closes the gap a GoTo leaves: expose, plate-solve, tell the mount where it really is,
// slew again. It is what makes a tile land where the plan says instead of an arcminute away.
async function center() {
  if (!props.tile) return;
  error.value = "";
  centering.value = true;
  centerResult.value = null;
  try {
    const res = await apiPost<{
      result: {
        centered: boolean;
        final_arcsec: number;
        attempts: { iteration: number; error_arcsec: number }[];
      };
      error?: string;
    }>("/api/capture/center", {
      ra_deg: props.tile.ra_deg,
      dec_deg: props.tile.dec_deg,
    });
    centerResult.value = res.result;
    if (res.error) error.value = res.error;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    centering.value = false;
  }
}

async function stop() {
  error.value = "";
  try {
    await store.stopMount();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

onMounted(async () => {
  if (!props.autoSlew || !props.tile) return;
  await store.refreshMount();
  if (!connected.value) {
    error.value = t("capture.tile.noMount");
    return;
  }
  await slew();
});
</script>

<template>
  <section
    v-if="tile"
    class="rounded-lg border border-brand-500/40 bg-brand-50/40 p-3 dark:bg-brand-900/10"
  >
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-100">
        {{ t("capture.tile.title", { panel: tile.folder }) }}
        <span
          v-if="objectName"
          class="font-normal text-slate-500 dark:text-slate-400"
        >
          · {{ objectName }}</span
        >
      </h2>
      <span class="font-mono text-xs text-slate-600 dark:text-slate-300">
        {{ raToHMS(tile.ra_deg) }} · {{ decToDMS(tile.dec_deg) }}
      </span>
    </div>

    <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
      <template v-if="!connected">{{ t("capture.tile.noMount") }}</template>
      <template v-else-if="mount?.slewing">{{
        t("capture.tile.slewing")
      }}</template>
      <template v-else-if="done">{{ t("capture.tile.arrived") }}</template>
      <template v-else>{{ t("capture.tile.ready") }}</template>
    </p>

    <div class="mt-2 flex flex-wrap items-center gap-2">
      <button
        :class="btnPrimary"
        class="!px-3 !py-1 text-xs"
        :disabled="!connected || slewing"
        @click="slew"
      >
        {{ done ? t("capture.tile.slewAgain") : t("capture.tile.slew") }}
      </button>
      <button
        :class="btnPrimary"
        class="!px-3 !py-1 text-xs"
        :disabled="!connected || centering"
        @click="center"
      >
        {{ centering ? t("capture.tile.centering") : t("capture.tile.center") }}
      </button>
      <!-- Always present, never disabled: a stop control that can be unavailable is not a stop. -->
      <button
        class="rounded-md border border-danger-500 px-3 py-1 text-xs font-semibold text-danger-500 hover:bg-danger-500/10"
        @click="stop"
      >
        {{ t("capture.tile.stop") }}
      </button>
      <span v-if="mount" class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("capture.tile.now") }}
        <span class="font-mono"
          >{{ raToHMS(mount.ra_deg) }} · {{ decToDMS(mount.dec_deg) }}</span
        >
        <span v-if="mount.alt_deg !== undefined">
          · {{ Math.round(mount.alt_deg) }}°</span
        >
      </span>
    </div>

    <p
      v-if="centerResult"
      class="mt-1 text-xs text-slate-500 dark:text-slate-400"
    >
      {{
        centerResult.centered
          ? t("capture.tile.centered", {
              arcsec: centerResult.final_arcsec.toFixed(1),
              n: centerResult.attempts.length,
            })
          : t("capture.tile.centerFailed", {
              arcsec: centerResult.final_arcsec.toFixed(1),
            })
      }}
    </p>
    <p v-if="error" class="mt-1 text-xs text-danger-500">{{ error }}</p>
  </section>
</template>
