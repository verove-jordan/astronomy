<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import StatGrid from "@/components/Common/StatGrid.vue";
import MosaicSetupForm from "@/components/Mosaic/MosaicSetupForm.vue";
import MosaicTargetSearch from "@/components/Mosaic/MosaicTargetSearch.vue";
import { btnGhost, btnPrimary, card, input } from "@/constants/styles";
import { ApiError } from "@/services/api";
import { useMosaicStore } from "@/stores/mosaic";
import { tangentPlane } from "@/utils/astro";
import { movedCenter } from "@/utils/skygrid";

// The planner knobs: object summary, rig, overlap/camera-angle/grid controls and the save panel.
// Every change is picked up by MosaicPlanner's deep draft watcher → debounced server recompute.
const { t } = useI18n();
const store = useMosaicStore();

const q = computed(() => store.preview?.query ?? null);
const grid = computed(() => store.preview?.grid ?? null);

const objectPa = computed(() => q.value?.object_pa_deg);
function alignToObject() {
  if (objectPa.value === undefined) return;
  store.draft.cameraPaDeg = Math.round(((objectPa.value + 90) % 360) * 10) / 10;
}

const overlapArcmin = computed(() => {
  if (!grid.value) return "";
  const w = grid.value.tile_w_deg * 60 * grid.value.overlap_frac;
  return `≈ ${w.toFixed(0)}′`;
});

// Mosaic footprint: tiles span (count−1) steps + one tile per axis.
const stats = computed(() => {
  const g = grid.value;
  if (!g) return [];
  const w = g.tile_w_deg + (g.cols - 1) * g.step_w_deg;
  const h = g.tile_h_deg + (g.rows - 1) * g.step_h_deg;
  return [
    { label: t("mosaic.controls.tiles"), value: `${g.cols}×${g.rows}` },
    {
      label: t("mosaic.controls.footprint"),
      value: `${w.toFixed(2)}°×${h.toFixed(2)}°`,
    },
    {
      label: t("mosaic.controls.tileFov"),
      value: `${g.tile_w_deg.toFixed(2)}°×${g.tile_h_deg.toFixed(2)}°`,
    },
    {
      label: t("mosaic.controls.scale"),
      value: `${(q.value?.image_scale_arcsec_px ?? 0).toFixed(2)}″/px`,
    },
  ];
});

// --- Framing (hand-moving the grid off the object) --------------------------------------------
// The map drag is the fast way; these are the precise (and mobile/touch-friendly) way, and they are
// also the fallback if the map fails to load.
const nudgeArcmin = ref(5);

const frameOffset = computed(() => {
  const query = q.value;
  if (!query || !query.center_moved) return null;
  const flat = tangentPlane(
    query.ra_deg,
    query.dec_deg,
    query.center_ra_deg,
    query.center_dec_deg,
  );
  if (!flat) return null;
  return { east: flat.xi * 60, north: flat.eta * 60 };
});

// nudge shifts the grid centre by whole arcminutes east/north in the tangent plane, starting from
// wherever the grid currently sits (the object itself, until it has been moved).
function nudge(eastArcmin: number, northArcmin: number) {
  const query = q.value;
  if (!query) return;
  const from = {
    ra: store.draft.centerRaDeg ?? query.center_ra_deg ?? query.ra_deg,
    dec: store.draft.centerDecDeg ?? query.center_dec_deg ?? query.dec_deg,
  };
  const moved = movedCenter(from, eastArcmin / 60, northArcmin / 60);
  store.draft.centerRaDeg = moved.ra;
  store.draft.centerDecDeg = moved.dec;
}

function centreOnObject() {
  store.draft.centerRaDeg = undefined;
  store.draft.centerDecDeg = undefined;
}

const showCustom = ref(false);
const planName = ref("");
const saveError = ref("");
const saveNotice = ref("");

async function save() {
  saveError.value = "";
  saveNotice.value = "";
  const name = planName.value.trim() || store.draft.targetName;
  if (!name) {
    saveError.value = t("mosaic.controls.nameRequired");
    return;
  }
  try {
    await store.savePlan(name);
    planName.value = "";
    saveNotice.value = t("mosaic.controls.saved");
  } catch (e) {
    saveError.value =
      e instanceof ApiError && e.status === 409
        ? t("mosaic.controls.nameExists")
        : String(e instanceof Error ? e.message : e);
  }
}

async function update() {
  saveError.value = "";
  saveNotice.value = "";
  // Re-framing re-points every tile, so tiles already shot no longer match what the plan says.
  // The server resets their status; make that explicit before it happens rather than after.
  const done = (store.progress?.captured ?? 0) + (store.progress?.skipped ?? 0);
  if (
    done > 0 &&
    !window.confirm(t("mosaic.framing.movedWarning", { n: done }))
  ) {
    return;
  }
  try {
    const reset = await store.updatePlanGeometry();
    saveNotice.value = reset
      ? t("mosaic.controls.statusesReset")
      : t("mosaic.controls.updated");
  } catch (e) {
    saveError.value = String(e instanceof Error ? e.message : e);
  }
}
</script>

<template>
  <div :class="card" class="space-y-4">
    <!-- Object -->
    <div>
      <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("mosaic.controls.object") }}
      </h2>
      <MosaicTargetSearch class="mt-1" />
      <p class="mt-2 text-sm text-slate-600 dark:text-slate-300">
        <span class="font-medium">{{
          q?.target || t("mosaic.controls.customTarget")
        }}</span>
        <span v-if="q && q.size_arcmin > 0" class="text-slate-400">
          · {{ q.size_arcmin.toFixed(0) }}′<template v-if="q.size_minor_arcmin"
            >×{{ q.size_minor_arcmin.toFixed(0) }}′</template
          ></span
        >
        <span v-if="objectPa !== undefined" class="text-slate-400">
          ·
          {{ t("mosaic.controls.objectPa", { pa: objectPa.toFixed(0) }) }}</span
        >
      </p>
      <router-link
        :to="{ name: 'tonight' }"
        class="text-xs text-brand-600 hover:underline dark:text-brand-300"
        >{{ t("mosaic.controls.changeObject") }}</router-link
      >
      <button
        class="ml-3 text-xs text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
        @click="showCustom = !showCustom"
      >
        {{ t("mosaic.controls.customToggle") }}
      </button>
      <div v-if="showCustom" class="mt-2 grid grid-cols-2 gap-2">
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.customRa") }}
          <input
            v-model.number="store.draft.customRaDeg"
            type="number"
            step="0.001"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.customDec") }}
          <input
            v-model.number="store.draft.customDecDeg"
            type="number"
            step="0.001"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.customSize") }}
          <input
            v-model.number="store.draft.customSizeArcmin"
            type="number"
            min="0"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.customSizeMinor") }}
          <input
            v-model.number="store.draft.customSizeMinorArcmin"
            type="number"
            min="0"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.customPa") }}
          <input
            v-model.number="store.draft.customObjectPaDeg"
            type="number"
            min="0"
            max="359.9"
            :class="input"
        /></label>
      </div>
    </div>

    <!-- Equipment -->
    <MosaicSetupForm />

    <!-- Geometry knobs -->
    <div class="space-y-3">
      <label class="block text-sm text-slate-600 dark:text-slate-300">
        <span class="flex items-center justify-between">
          <span>{{ t("mosaic.controls.overlap") }}</span>
          <span class="text-xs text-slate-400"
            >{{ store.draft.overlapPct }}% {{ overlapArcmin }}</span
          >
        </span>
        <input
          v-model.number="store.draft.overlapPct"
          type="range"
          min="5"
          max="50"
          step="1"
          class="mt-1 w-full accent-brand-600"
        />
      </label>

      <div>
        <span class="text-sm text-slate-600 dark:text-slate-300">{{
          t("mosaic.controls.cameraPa")
        }}</span>
        <div class="mt-1 flex items-center gap-2">
          <input
            v-model.number="store.draft.cameraPaDeg"
            type="number"
            min="0"
            max="359.9"
            step="0.1"
            :class="input"
            class="w-24"
          />
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            @click="store.draft.cameraPaDeg = 0"
          >
            {{ t("mosaic.controls.northUp") }}
          </button>
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :disabled="objectPa === undefined"
            :title="objectPa === undefined ? t('mosaic.controls.noPa') : ''"
            @click="alignToObject"
          >
            {{ t("mosaic.controls.alignToObject") }}
          </button>
        </div>
      </div>

      <!-- Framing -->
      <div>
        <span class="text-sm text-slate-600 dark:text-slate-300">{{
          t("mosaic.framing.title")
        }}</span>
        <div class="mt-1 flex flex-wrap items-center gap-1">
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :title="t('mosaic.framing.east')"
            @click="nudge(nudgeArcmin, 0)"
          >
            ← E
          </button>
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :title="t('mosaic.framing.north')"
            @click="nudge(0, nudgeArcmin)"
          >
            ↑ N
          </button>
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :title="t('mosaic.framing.south')"
            @click="nudge(0, -nudgeArcmin)"
          >
            ↓ S
          </button>
          <button
            :class="btnGhost"
            class="!px-2 !py-1 text-xs"
            :title="t('mosaic.framing.west')"
            @click="nudge(-nudgeArcmin, 0)"
          >
            W →
          </button>
          <select v-model.number="nudgeArcmin" :class="input" class="w-20">
            <option :value="1">1′</option>
            <option :value="5">5′</option>
            <option :value="15">15′</option>
            <option :value="30">30′</option>
          </select>
        </div>
        <p class="mt-1 text-[11px] text-slate-400">
          <template v-if="frameOffset">
            {{
              t("mosaic.framing.offset", {
                east: frameOffset.east.toFixed(1),
                north: frameOffset.north.toFixed(1),
              })
            }}
            <button
              class="ml-1 text-brand-600 hover:underline dark:text-brand-300"
              @click="centreOnObject"
            >
              {{ t("mosaic.framing.recentre") }}
            </button>
          </template>
          <template v-else>{{ t("mosaic.framing.centred") }}</template>
        </p>
      </div>

      <div class="grid grid-cols-3 gap-2">
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.cols") }}
          <input
            v-model.number="store.draft.colsOverride"
            type="number"
            min="0"
            max="8"
            :placeholder="t('mosaic.controls.auto')"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.rows") }}
          <input
            v-model.number="store.draft.rowsOverride"
            type="number"
            min="0"
            max="8"
            :placeholder="t('mosaic.controls.auto')"
            :class="input"
        /></label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("mosaic.controls.margin") }}
          <input
            v-model.number="store.draft.marginArcmin"
            type="number"
            min="0"
            max="120"
            :class="input"
        /></label>
      </div>
      <p class="text-[11px] text-slate-400">
        {{ t("mosaic.controls.autoHint") }}
      </p>
    </div>

    <StatGrid v-if="stats.length" :items="stats" :cols="2" />

    <!-- Save -->
    <div class="space-y-2 border-t border-slate-200 pt-3 dark:border-slate-700">
      <div class="flex gap-2">
        <input
          v-model="planName"
          :class="input"
          :placeholder="t('mosaic.controls.namePlaceholder')"
        />
        <button
          :class="btnPrimary"
          :disabled="store.busy || !store.preview"
          @click="save"
        >
          {{ t("mosaic.controls.save") }}
        </button>
      </div>
      <button
        v-if="store.activePlan"
        :class="btnGhost"
        class="w-full"
        :disabled="store.busy"
        @click="update"
      >
        {{ t("mosaic.controls.update", { name: store.activePlan.name }) }}
      </button>
      <p v-if="saveError" class="text-xs text-danger-500">{{ saveError }}</p>
      <p v-if="saveNotice" class="text-xs text-green-600 dark:text-green-400">
        {{ saveNotice }}
      </p>
    </div>
  </div>
</template>
