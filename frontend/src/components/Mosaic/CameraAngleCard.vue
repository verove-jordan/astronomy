<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import StarFieldCanvas from "@/components/Mosaic/StarFieldCanvas.vue";
import { btnGhost, btnPrimary, card, checkbox } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";

// Step 0 of a mosaic session: set the camera rotation ONCE at the mount's home position (tube on
// the celestial pole). The chart shows the star field a correctly-rotated camera sees there — the
// user takes a short exposure and turns the camera in the focuser until the frames match, then
// locks it for the whole mosaic (EQ mount ⇒ no field rotation ⇒ one angle fits every tile).
const { t } = useI18n();
const store = useMosaicStore();

const plan = computed(() => store.activePlan);
const mirrored = ref(false);
const err = ref("");

// The visible pole for the plan's site (dec ±90; RA is degenerate there — 0 by convention).
const poleDec = computed(() =>
  (plan.value?.request.lat ?? 90) >= 0 ? 90 : -90,
);
const fov = computed(() => {
  const g = plan.value?.grid;
  if (!g) return 3;
  return 2 * Math.hypot(g.tile_w_deg, g.tile_h_deg);
});

const steps = ["step1", "step2", "step3", "step4"] as const;

async function confirm(done: boolean) {
  err.value = "";
  try {
    await store.setOrientationDone(done);
  } catch (e) {
    err.value = String(e instanceof Error ? e.message : e);
  }
}
</script>

<template>
  <div
    v-if="plan"
    :class="[
      card,
      plan.orientation_done
        ? 'border-l-4 border-l-green-500'
        : 'border-l-4 border-l-brand-500 ring-1 ring-brand-500/40',
    ]"
  >
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("mosaic.orientation.title") }}
      </h2>
      <span
        class="font-mono text-lg font-bold text-brand-600 dark:text-brand-300"
      >
        {{
          t("mosaic.orientation.paReadout", {
            pa: plan.grid.camera_pa_deg.toFixed(1),
          })
        }}
      </span>
    </div>

    <div class="mt-3 grid gap-4 sm:grid-cols-2">
      <div class="aspect-square">
        <StarFieldCanvas
          :center-ra-deg="0"
          :center-dec-deg="poleDec"
          :fov-deg="fov"
          :rects="[
            {
              wDeg: plan.grid.tile_w_deg,
              hDeg: plan.grid.tile_h_deg,
              paDeg: plan.grid.camera_pa_deg,
            },
          ]"
          :mirrored="mirrored"
          class="h-full"
        />
      </div>
      <div>
        <ol
          class="list-decimal space-y-2 pl-5 text-sm text-slate-600 dark:text-slate-300"
        >
          <li v-for="s in steps" :key="s">
            {{ t(`mosaic.orientation.${s}`) }}
          </li>
        </ol>
        <label
          class="mt-3 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400"
        >
          <input v-model="mirrored" type="checkbox" :class="checkbox" />
          {{ t("mosaic.orientation.mirror") }}
        </label>
      </div>
    </div>

    <div class="mt-3 flex items-center justify-between">
      <button
        v-if="!plan.orientation_done"
        :class="btnPrimary"
        @click="confirm(true)"
      >
        {{ t("mosaic.orientation.confirm") }}
      </button>
      <span
        v-else
        class="text-xs font-medium text-green-600 dark:text-green-400"
        >✓ {{ t("mosaic.orientation.done") }}</span
      >
      <button
        v-if="plan.orientation_done"
        :class="btnGhost"
        class="!px-2 !py-1 text-xs"
        @click="confirm(false)"
      >
        {{ t("mosaic.orientation.redo") }}
      </button>
    </div>
    <p v-if="err" class="mt-1 text-xs text-danger-500">{{ err }}</p>
  </div>
</template>
