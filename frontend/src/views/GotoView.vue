<script setup lang="ts">
import { onMounted, ref, computed, watch, defineAsyncComponent } from "vue";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import { useSkyStore } from "@/stores/sky";
import AlignmentSequence from "@/components/Goto/AlignmentSequence.vue";
import MosaicContinueCard from "@/components/Mosaic/MosaicContinueCard.vue";
import AlignmentSkyMap from "@/components/Goto/AlignmentSkyMap.vue";

// The interactive sky map bundles a raw canvas + the star/constellation dataset — load it lazily so it
// only enters the /goto chunk when this view mounts.
const StarChart = defineAsyncComponent(
  () => import("@/components/Goto/StarChart.vue"),
);
import StarChartModal from "@/components/Goto/StarChartModal.vue";
import MountTuningPanel from "@/components/Goto/MountTuningPanel.vue";
import PolarScopeReticle from "@/components/Polar/PolarScopeReticle.vue";
import PolarAlignPanel from "@/components/Polar/PolarAlignPanel.vue";
import PolarTutorial from "@/components/Polar/PolarTutorial.vue";
import Spinner from "@/components/Common/Spinner.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import { card, input, btnGhost } from "@/constants/styles";
import { tzForLocation, zonedWallToISO, nowInZone } from "@/utils/tz";

// Leaflet is heavy — load the map lazily so it doesn't bloat the route's initial chunk.
const LocationPicker = defineAsyncComponent(
  () => import("@/components/Sky/LocationPicker.vue"),
);

const { t } = useI18n();
const store = useGotoStore();
const sky = useSkyStore();

onMounted(() => {
  void store.fetchProfiles(); // mount/routine registry (count bounds, phases, HC star list)
  void store.fetch();
});

const loc = computed(() => store.query?.location);
const tz = computed(() =>
  loc.value
    ? tzForLocation(loc.value.lat, loc.value.lon)
    : Intl.DateTimeFormat().resolvedOptions().timeZone,
);

const profile = computed({
  get: () => store.params.profile ?? "eq-generic",
  set: (v: string) => {
    store.setProfile(v);
  },
});

// The backend clamps the count to the profile's range and echoes it, so display the effective value.
const effCount = computed(() => store.query?.count ?? store.params.count ?? 3);
function changeCount(delta: number) {
  store.setCount(Math.max(1, effCount.value + delta));
}

// Two-phase routines (Celestron EQ): break the total down as "N align + M calibration" so the count
// stepper reads like the hand-controller procedure.
const phaseHint = computed(() => {
  const alignStars = store.currentProfile?.align_stars ?? 0;
  if (alignStars <= 0) return "";
  return t("goto.controls.phaseHint", {
    align: Math.min(alignStars, effCount.value),
    calib: Math.max(0, effCount.value - alignStars),
  });
});

// Time: "now" (default) or a specific instant entered in the site's local time (mirrors Polar/Tonight).
const useCustomTime = ref(false);
const customLocal = ref("");
function applyTime() {
  if (useCustomTime.value && customLocal.value) {
    store.setTime(zonedWallToISO(customLocal.value, tz.value));
  }
}
watch(useCustomTime, (on) => {
  if (on) {
    customLocal.value = nowInZone(
      store.query?.at_utc_ms ?? Date.now(),
      tz.value,
    );
    applyTime();
  } else {
    store.setTime(undefined);
  }
});
watch(customLocal, () => applyTime());

// Follow the observing site chosen elsewhere in the app (unless a specific time is pinned).
watch(
  () => sky.query?.location,
  (l, prev) => {
    if (
      !useCustomTime.value &&
      l &&
      prev &&
      (l.lat !== prev.lat || l.lon !== prev.lon)
    ) {
      store.fetch(undefined, true);
    }
  },
);

function onPick(lat: number, lon: number) {
  store.setLocation(lat, lon);
}

// Fullscreen sky map (a second StarChart instance in a modal filling the content area).
const mapExpanded = ref(false);
</script>

<template>
  <div class="space-y-4">
    <header>
      <div class="flex items-center gap-2">
        <h1 class="text-xl font-bold text-brand-300">{{ t("goto.title") }}</h1>
        <HelpButton />
      </div>
      <p class="text-sm text-slate-400">{{ t("goto.subtitle") }}</p>
    </header>

    <!-- Step 1 — Mise en station: polar-align the mount before building the GoTo model. -->
    <section class="space-y-3">
      <div>
        <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
          {{ t("polar.title") }}
        </h2>
        <p class="text-xs text-slate-400">{{ t("polar.subtitle") }}</p>
      </div>
      <div class="grid gap-4 lg:grid-cols-2">
        <PolarScopeReticle />
        <PolarAlignPanel />
      </div>
    </section>

    <MosaicContinueCard />

    <!-- Step 2 — GoTo star alignment. -->
    <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
      {{ t("goto.sequence.title") }}
    </h2>

    <!-- Controls -->
    <div :class="card">
      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label class="mb-1 block text-xs font-medium text-slate-400">{{
            t("goto.controls.location")
          }}</label>
          <LocationPicker
            v-if="loc"
            :lat="loc.lat"
            :lon="loc.lon"
            @pick="onPick"
          />
        </div>
        <div class="space-y-3">
          <div>
            <label class="mb-1 block text-xs font-medium text-slate-400">{{
              t("goto.controls.mount")
            }}</label>
            <select v-model="profile" :class="input" data-demo="goto-catalogue">
              <option v-for="p in store.profiles" :key="p.key" :value="p.key">
                {{ t(`goto.profiles.${p.key}.label`) }}
              </option>
            </select>
            <p class="mt-1 text-[11px] text-slate-400">
              {{ t(`goto.profiles.${profile}.note`) }}
            </p>
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-slate-400">{{
              t("goto.controls.count")
            }}</label>
            <div class="flex items-center gap-2">
              <button :class="[btnGhost, 'px-3 py-1']" @click="changeCount(-1)">
                −
              </button>
              <span class="w-8 text-center text-lg font-bold tabular-nums">{{
                effCount
              }}</span>
              <button :class="[btnGhost, 'px-3 py-1']" @click="changeCount(1)">
                +
              </button>
              <span v-if="phaseHint" class="text-[11px] text-slate-400">{{
                phaseHint
              }}</span>
            </div>
          </div>
          <div>
            <label class="mb-1 block text-xs font-medium text-slate-400">{{
              t("goto.controls.time")
            }}</label>
            <div class="flex flex-wrap items-center gap-3">
              <label class="flex items-center gap-1.5 text-xs text-slate-400">
                <input
                  v-model="useCustomTime"
                  type="checkbox"
                  class="accent-brand-500"
                />
                {{ t("goto.controls.specific") }}
              </label>
              <input
                v-if="useCustomTime"
                v-model="customLocal"
                type="datetime-local"
                :class="[input, 'w-auto text-xs']"
              />
              <span v-else-if="store.query" class="text-xs text-slate-400">{{
                store.query.at_local
              }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Body -->
    <div
      v-if="store.loading && !store.stars.length"
      class="flex justify-center py-8"
    >
      <Spinner />
    </div>
    <div class="grid gap-4 lg:grid-cols-2">
      <AlignmentSequence />
      <div class="space-y-4">
        <div :class="card">
          <h3
            class="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200"
          >
            {{ t("goto.sky.title") }}
          </h3>
          <p class="mb-2 text-xs text-slate-400">
            {{ t("goto.sky.subtitle") }}
          </p>
          <StarChart @expand="mapExpanded = true" />
        </div>
        <div :class="card">
          <h3
            class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
          >
            {{ t("goto.map.title") }}
          </h3>
          <AlignmentSkyMap />
        </div>
        <div :class="card">
          <h3
            class="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200"
          >
            {{ t("goto.why.title") }}
          </h3>
          <p class="text-xs text-slate-400">{{ t("goto.why.body") }}</p>
        </div>
      </div>
    </div>
    <p v-if="store.error" class="text-sm text-danger-500">{{ store.error }}</p>

    <!-- Reference material: the polar-scope setup walkthrough, collapsed by default. -->
    <PolarTutorial />

    <!-- Known mount issues & free compensations, keyed to the selected mount model. -->
    <MountTuningPanel />

    <!-- Fullscreen sky map, beside the nav rail. -->
    <StarChartModal v-if="mapExpanded" @close="mapExpanded = false">
      <StarChart fill />
    </StarChartModal>
  </div>
</template>
