<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";

import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import Spinner from "@/components/Common/Spinner.vue";
import TwoPane from "@/components/Common/TwoPane.vue";
import BodyInfoCard from "@/components/Solar/BodyInfoCard.vue";
import SolarSystemCanvas from "@/components/Solar/SolarSystemCanvas.vue";
import TimeScrubber from "@/components/Solar/TimeScrubber.vue";
import { useSimClock } from "@/composables/useSimClock";
import {
  btn,
  card,
  checkbox,
  segActive,
  segBtn,
  segIdle,
  segWrap,
} from "@/constants/styles";
import { useSolarSystemStore } from "@/stores/solarsystem";
import { RANGE_FROM, RANGE_TO } from "@/utils/solarsystem";

// The solar system as a place you can fly around, running at whatever speed you choose.
//
// Every control on this page feeds one of two things: the instant being drawn, or how the drawing is
// distorted to stay legible. Nothing here changes what is TRUE — the distortions are confined to the
// picture, and the readout beside it always reports the real geometry.

const { t } = useI18n();
const store = useSolarSystemStore();

const MIN_MS = Date.UTC(RANGE_FROM, 0, 1);
const MAX_MS = Date.UTC(RANGE_TO, 11, 31, 23, 59, 59);

const clock = useSimClock({ min: MIN_MS, max: MAX_MS });

// View state, persisted so the page opens the way you left it.
const STORE_KEY = "astrostack.solarsystem.view";
const warp = ref(1);
const moonScale = ref(12);
const exaggerate = ref(1);
const showOrbits = ref(true);
const showAxes = ref(false);
const showStars = ref(true);
const showLabels = ref(true);

const selected = ref<string | null>(null);
const hovered = ref<string | null>(null);
const follow = ref<string | null>(null);
const canvasRef = ref<InstanceType<typeof SolarSystemCanvas> | null>(null);

try {
  const saved = JSON.parse(localStorage.getItem(STORE_KEY) ?? "{}");
  if (typeof saved.warp === "number") warp.value = saved.warp;
  if (typeof saved.moonScale === "number") moonScale.value = saved.moonScale;
  if (typeof saved.exaggerate === "number") exaggerate.value = saved.exaggerate;
  if (typeof saved.showOrbits === "boolean")
    showOrbits.value = saved.showOrbits;
  if (typeof saved.showAxes === "boolean") showAxes.value = saved.showAxes;
  if (typeof saved.showStars === "boolean") showStars.value = saved.showStars;
  if (typeof saved.showLabels === "boolean")
    showLabels.value = saved.showLabels;
} catch {
  // A corrupt preference is not worth a broken page; the defaults above stand.
}

watch(
  [warp, moonScale, exaggerate, showOrbits, showAxes, showStars, showLabels],
  () => {
    localStorage.setItem(
      STORE_KEY,
      JSON.stringify({
        warp: warp.value,
        moonScale: moonScale.value,
        exaggerate: exaggerate.value,
        showOrbits: showOrbits.value,
        showAxes: showAxes.value,
        showStars: showStars.value,
        showLabels: showLabels.value,
      }),
    );
  },
);

onMounted(() => store.fetchManifest());

// The engine is asked what is true at most a few times a second — THROTTLED, not debounced.
//
// Debouncing is the obvious choice and the wrong one: a running clock changes the instant on every
// animation frame, so the timer resets forever and the request never fires at all. Throttling keeps
// the numbers arriving while time runs, and the trailing call is what makes a scrub that stops
// mid-gesture end on the instant it stopped at rather than the last one that happened to get through.
const STATE_INTERVAL_MS = 700;
let lastStateAt = 0;
let stateTimer: ReturnType<typeof setTimeout> | null = null;

function requestState(immediate = false) {
  if (stateTimer) {
    clearTimeout(stateTimer);
    stateTimer = null;
  }
  const wait = immediate
    ? 0
    : Math.max(0, STATE_INTERVAL_MS - (Date.now() - lastStateAt));
  stateTimer = setTimeout(() => {
    lastStateAt = Date.now();
    stateTimer = null;
    void store.fetchState(clock.timeMs.value);
  }, wait);
}

watch(
  () => clock.timeMs.value,
  () => requestState(),
  { immediate: true },
);
// Pausing, or landing on an exact date, is someone about to read the numbers: answer at once.
watch(
  () => clock.playing.value,
  (playing) => !playing && requestState(true),
);
watch(
  () => store.manifest,
  () => requestState(true),
);

onBeforeUnmount(() => {
  if (stateTimer) clearTimeout(stateTimer);
});

/**
 * How far the readout's instant is from the one on screen. At a year per second the engine cannot
 * be asked often enough to keep up, and quietly showing last second's geometry beside this second's
 * picture is the kind of wrong that looks right — so the card says which instant it is describing.
 */
const stateLagMs = computed(() => {
  const at = store.snapshot?.time_ms;
  return at === undefined ? 0 : clock.timeMs.value - at;
});

const shown = computed(() => hovered.value ?? selected.value);
const shownBody = computed(() =>
  shown.value ? store.byKey.get(shown.value) : undefined,
);
const shownState = computed(() =>
  shown.value ? store.stateFor(shown.value) : null,
);
const pinned = computed(() => !hovered.value && !!selected.value);

/** The picker's groups: the Sun and the planets, each with its own moons folded under it. */
const groups = computed(() => {
  const out: { parent: string; children: string[] }[] = [];
  for (const b of store.bodies) {
    if (b.kind === "moon") {
      out[out.length - 1]?.children.push(b.key);
    } else {
      out.push({ parent: b.key, children: [] });
    }
  }
  return out;
});

function choose(key: string) {
  selected.value = key;
  canvasRef.value?.frameBody(key);
}

function resetView() {
  selected.value = null;
  follow.value = null;
  canvasRef.value?.home();
}

const distanceMode = computed({
  get: () =>
    warp.value < 0.02 ? "true" : warp.value > 0.98 ? "even" : "mixed",
  set: (v: string) => {
    warp.value = v === "true" ? 0 : v === "even" ? 1 : 0.5;
  },
});
</script>

<template>
  <div class="space-y-4">
    <div>
      <div class="flex items-center gap-2">
        <h1 class="text-2xl font-semibold">{{ t("solarsystem.title") }}</h1>
        <HelpButton />
      </div>
      <p class="text-sm text-slate-400">{{ t("solarsystem.subtitle") }}</p>
    </div>

    <p
      v-if="store.versionMismatch"
      class="rounded-md bg-warning-600/15 px-3 py-2 text-sm text-warning-300"
    >
      {{ t("solarsystem.versionMismatch") }}
    </p>
    <p
      v-else-if="store.error"
      class="rounded-md bg-danger-600/15 px-3 py-2 text-sm text-danger-300"
    >
      {{ store.error }}
    </p>

    <div
      v-if="store.loading && !store.manifest"
      class="flex items-center gap-2 text-sm text-slate-400"
    >
      <Spinner />
      {{ t("solarsystem.loading") }}
    </div>

    <TwoPane v-else split="main-aside" breakpoint="xl" sticky-aside>
      <template #main>
        <div class="space-y-4">
          <SolarSystemCanvas
            ref="canvasRef"
            :manifest="store.manifest"
            :time-ms="clock.timeMs.value"
            :warp="warp"
            :moon-scale="moonScale"
            :exaggerate="exaggerate"
            :show-orbits="showOrbits"
            :show-axes="showAxes"
            :show-stars="showStars"
            :show-labels="showLabels"
            :follow="follow"
            :selected="selected"
            @update:selected="selected = $event"
            @update:follow="follow = $event"
            @hover="hovered = $event"
          />

          <section :class="card">
            <TimeScrubber :clock="clock" />
          </section>

          <section :class="card" data-demo="solar-layers">
            <h2 class="mb-3 text-sm font-semibold text-slate-200">
              {{ t("solarsystem.view.title") }}
            </h2>

            <div class="grid gap-4 sm:grid-cols-2">
              <div class="space-y-1">
                <span class="text-xs text-slate-400">{{
                  t("solarsystem.view.distances")
                }}</span>
                <div :class="segWrap">
                  <button
                    type="button"
                    :class="[
                      segBtn,
                      distanceMode === 'true' ? segActive : segIdle,
                    ]"
                    @click="distanceMode = 'true'"
                  >
                    {{ t("solarsystem.view.trueScale") }}
                  </button>
                  <button
                    type="button"
                    :class="[
                      segBtn,
                      distanceMode === 'mixed' ? segActive : segIdle,
                    ]"
                    @click="distanceMode = 'mixed'"
                  >
                    {{ t("solarsystem.view.mixed") }}
                  </button>
                  <button
                    type="button"
                    :class="[
                      segBtn,
                      distanceMode === 'even' ? segActive : segIdle,
                    ]"
                    @click="distanceMode = 'even'"
                  >
                    {{ t("solarsystem.view.compressed") }}
                  </button>
                </div>
                <input
                  v-model.number="warp"
                  type="range"
                  min="0"
                  max="1"
                  step="0.01"
                  class="w-full accent-brand-500"
                  :aria-label="t('solarsystem.view.distances')"
                />
                <p class="text-[11px] leading-snug text-slate-500">
                  {{ t("solarsystem.view.distancesHint") }}
                </p>
              </div>

              <div class="space-y-1">
                <label class="block">
                  <span
                    class="mb-1 flex items-baseline justify-between text-xs text-slate-400"
                  >
                    <span>{{ t("solarsystem.view.bodySize") }}</span>
                    <span class="tabular-nums text-slate-300"
                      >×{{ exaggerate.toFixed(0) }}</span
                    >
                  </span>
                  <input
                    v-model.number="exaggerate"
                    type="range"
                    min="1"
                    max="500"
                    step="1"
                    class="w-full accent-brand-500"
                  />
                </label>
                <label class="block">
                  <span
                    class="mb-1 flex items-baseline justify-between text-xs text-slate-400"
                  >
                    <span>{{ t("solarsystem.view.moonScale") }}</span>
                    <span class="tabular-nums text-slate-300"
                      >×{{ moonScale.toFixed(0) }}</span
                    >
                  </span>
                  <input
                    v-model.number="moonScale"
                    type="range"
                    min="1"
                    max="60"
                    step="1"
                    class="w-full accent-brand-500"
                  />
                </label>
              </div>
            </div>

            <div
              class="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2 text-sm"
            >
              <label class="flex items-center gap-2">
                <input v-model="showOrbits" type="checkbox" :class="checkbox" />
                {{ t("solarsystem.view.orbits") }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="showAxes" type="checkbox" :class="checkbox" />
                {{ t("solarsystem.view.axes") }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="showStars" type="checkbox" :class="checkbox" />
                {{ t("solarsystem.view.stars") }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="showLabels" type="checkbox" :class="checkbox" />
                {{ t("solarsystem.view.labels") }}
              </label>
              <button
                type="button"
                :class="btn"
                class="ml-auto bg-slate-700 text-slate-100 hover:bg-slate-600"
                @click="resetView"
              >
                {{ t("solarsystem.view.reset") }}
              </button>
            </div>
          </section>
        </div>
      </template>

      <template #aside>
        <div class="space-y-4">
          <section :class="card">
            <BodyInfoCard
              v-if="shownBody"
              :body="shownBody"
              :state="shownState"
              :pinned="pinned"
              :lag-ms="stateLagMs"
              :state-at-ms="store.snapshot?.time_ms"
              @unpin="selected = null"
            />
            <p v-else class="text-sm text-slate-400">
              {{ t("solarsystem.info.empty") }}
            </p>
          </section>

          <CollapsibleCard
            :title="t('solarsystem.picker.title')"
            storage-key="astrostack.solarsystem.picker"
            :default-open="true"
          >
            <ul class="space-y-0.5 text-sm" data-demo="solar-bodies">
              <li v-for="g in groups" :key="g.parent">
                <button
                  type="button"
                  class="flex w-full items-center gap-2 rounded px-2 py-1 text-left hover:bg-slate-700/50"
                  :class="
                    selected === g.parent
                      ? 'bg-brand-600/25 text-white'
                      : 'text-slate-200'
                  "
                  @click="choose(g.parent)"
                >
                  <span
                    class="h-2.5 w-2.5 shrink-0 rounded-full"
                    :style="{
                      backgroundColor: store.byKey.get(g.parent)?.colour,
                    }"
                    aria-hidden="true"
                  />
                  {{ t(`solarsystem.bodies.${g.parent}`) }}
                </button>
                <ul v-if="g.children.length" class="ml-5 space-y-0.5">
                  <li v-for="c in g.children" :key="c">
                    <button
                      type="button"
                      class="flex w-full items-center gap-2 rounded px-2 py-0.5 text-left text-xs hover:bg-slate-700/50"
                      :class="
                        selected === c
                          ? 'bg-brand-600/25 text-white'
                          : 'text-slate-400'
                      "
                      @click="choose(c)"
                    >
                      <span
                        class="h-2 w-2 shrink-0 rounded-full"
                        :style="{ backgroundColor: store.byKey.get(c)?.colour }"
                        aria-hidden="true"
                      />
                      {{ t(`solarsystem.bodies.${c}`) }}
                    </button>
                  </li>
                </ul>
              </li>
            </ul>
          </CollapsibleCard>

          <CollapsibleCard
            :title="t('solarsystem.legend.title')"
            storage-key="astrostack.solarsystem.legend"
          >
            <div class="space-y-2 text-xs leading-relaxed text-slate-400">
              <p>{{ t("solarsystem.legend.warpNote") }}</p>
              <p>{{ t("solarsystem.legend.sizeNote") }}</p>
              <p>{{ t("solarsystem.legend.tierNote") }}</p>
              <p v-if="store.manifest">
                {{
                  t("solarsystem.legend.range", {
                    from: store.manifest.range_from,
                    to: store.manifest.range_to,
                  })
                }}
              </p>
              <ul class="space-y-1 pt-1">
                <li v-for="s in store.manifest?.sources ?? []" :key="s.name">
                  <span class="text-slate-300">{{ s.name }}</span> —
                  {{ s.covers }}
                  <span class="text-slate-500">({{ s.licence }})</span>
                </li>
              </ul>
            </div>
          </CollapsibleCard>
        </div>
      </template>
    </TwoPane>
  </div>
</template>
