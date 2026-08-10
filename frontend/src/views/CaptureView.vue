<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute } from "vue-router";
import TwoPane from "@/components/Common/TwoPane.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import CameraControls from "@/components/Capture/CameraControls.vue";
import DevicePanel from "@/components/Capture/DevicePanel.vue";
import FocusMeter from "@/components/Capture/FocusMeter.vue";
import CalibrationWizard from "@/components/Capture/CalibrationWizard.vue";
import CaptureSessions from "@/components/Capture/CaptureSessions.vue";
import FilterSlots from "@/components/Capture/FilterSlots.vue";
import TrackingReport from "@/components/Capture/TrackingReport.vue";
import LiveHistogram from "@/components/Capture/LiveHistogram.vue";
import LiveView from "@/components/Capture/LiveView.vue";
import MountPanel from "@/components/Capture/MountPanel.vue";
import PolarCameraPanel from "@/components/Capture/PolarCameraPanel.vue";
import SequenceRunner from "@/components/Capture/SequenceRunner.vue";
import TileTargetCard from "@/components/Capture/TileTargetCard.vue";
import { card } from "@/constants/styles";
import { useCaptureStore } from "@/stores/capture";
import { useMosaicStore } from "@/stores/mosaic";

// The capture page: everything needed at the telescope, on one screen — what is connected, what the
// camera sees right now, how sharp it is, and the auto-run that shoots the night.
//
// Reached from a mosaic tile with ?plan=&tile=, in which case the destination folder and target are
// pre-filled from the plan, so a tile's frames land in the folder the stacker segments on.
const { t } = useI18n();
const route = useRoute();
const store = useCaptureStore();
const mosaic = useMosaicStore();

onMounted(async () => {
  await store.refreshDevices();
  await Promise.all([store.refreshProgress(), store.loadSequences()]);
  store.watchProgress();
  // Keeps the sensor temperature and cooler power ticking, so the cooling panel shows a trend rather
  // than the value it happened to have at connect time.
  store.watchDevices();
  const planId = Number(route.query.plan);
  if (planId) {
    try {
      await mosaic.loadPlan(planId, true);
    } catch {
      // the plan was removed; the page still works as a plain capture screen
    }
  }
});

onBeforeUnmount(() => {
  store.stopWatching();
});

// Mosaic context: which tile is being shot, and where its frames belong. Arriving with goto=1 (the
// tile table's Capture button) also points the telescope at it.
const tileIndex = computed(() => Number(route.query.tile));
const tile = computed(
  () =>
    mosaic.activePlan?.tiles.find((x) => x.index === tileIndex.value) ?? null,
);
const panel = computed(() => tile.value?.folder);
const autoSlew = computed(() => route.query.goto === "1");
const capturePath = computed(() => mosaic.activePlan?.capture_root || "");
const objectName = computed(() => mosaic.activePlan?.object_name ?? "");
// Fullscreen framing/focus mode.
//
// At the telescope the preview is the thing you stare at, and the settings you reach for while
// staring are few: exposure, gain, the filter. So fullscreen gives the frame the whole page and
// floats those controls over it, fading them out when the mouse rests so nothing covers the stars —
// and back in the moment it moves.
const fullscreen = ref(false);
const chromeVisible = ref(true);
let chromeTimer = 0;
// Kept visible while the pointer is in the panel or a field has focus: controls that vanish mid-edit
// are worse than controls that never hide.
const chromePinned = ref(false);

function revealChrome() {
  chromeVisible.value = true;
  window.clearTimeout(chromeTimer);
  if (chromePinned.value) return;
  chromeTimer = window.setTimeout(() => {
    if (!chromePinned.value) chromeVisible.value = false;
  }, 2500);
}

function enterFullscreen() {
  fullscreen.value = true;
  revealChrome();
}

function exitFullscreen() {
  fullscreen.value = false;
  window.clearTimeout(chromeTimer);
  chromeVisible.value = true;
}

function onFullscreenKey(e: KeyboardEvent) {
  if (e.key === "Escape" && fullscreen.value) exitFullscreen();
}

onMounted(() => window.addEventListener("keydown", onFullscreenKey));
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onFullscreenKey);
  window.clearTimeout(chromeTimer);
});

const imageScale = computed(
  () => mosaic.preview?.query.image_scale_arcsec_px ?? undefined,
);
</script>

<template>
  <div class="space-y-4">
    <header>
      <div class="flex items-center gap-2">
        <h1 class="text-xl font-semibold text-slate-800 dark:text-slate-100">
          {{ t("capture.title") }}
        </h1>
        <HelpButton />
      </div>
      <p class="text-sm text-slate-500 dark:text-slate-400">
        {{ t("capture.subtitle") }}
      </p>
    </header>

    <TileTargetCard
      v-if="tile"
      :tile="tile"
      :object-name="objectName"
      :auto-slew="autoSlew"
      :plan-id="mosaic.activePlan?.id"
    />

    <TwoPane split="main-aside">
      <template #main>
        <div class="space-y-4">
          <!-- One LiveView instance, teleported into a full-page layer when fullscreen: moving the
               existing DOM keeps the canvas contents, the zoom state and the live session intact,
               where a second instance would start a second stream. -->
          <Teleport to="body" :disabled="!fullscreen">
            <section
              :class="
                fullscreen
                  ? 'fixed inset-0 z-50 flex bg-black'
                  : [card, 'relative block']
              "
              @mousemove="fullscreen && revealChrome()"
            >
              <h2
                v-if="!fullscreen"
                class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
              >
                {{ t("capture.live.title") }}
              </h2>

              <!-- Camera settings sit BESIDE the image, not in the sidebar below it. Exposure, gain
                   and the black point are all judged by looking at the frame, so having to scroll
                   away from the frame to change them made every adjustment a guess-and-check round
                   trip. Fullscreen floats the same column over the image instead. -->
              <div
                :class="
                  fullscreen
                    ? 'relative h-full w-full'
                    : 'flex flex-col gap-4 xl:flex-row'
                "
              >
                <div
                  :class="fullscreen ? 'h-full w-full' : 'min-w-0 flex-1'"
                  data-demo="capture-liveview"
                >
                  <LiveView
                    :fill="fullscreen"
                    :chrome-visible="chromeVisible"
                  />
                </div>

                <!-- Inline: a column to the right of the image. Fullscreen: the same controls
                     floating over it, fading out when the mouse rests so nothing covers the stars. -->
                <aside
                  v-if="store.connected.camera"
                  data-demo="capture-camera"
                  :class="[
                    fullscreen
                      ? 'absolute right-0 top-0 h-full w-80 overflow-y-auto border-l border-white/10 bg-slate-950/85 p-4 backdrop-blur transition-opacity duration-300'
                      : 'shrink-0 border-slate-200 dark:border-slate-700 xl:w-72 xl:border-l xl:pl-4',
                    fullscreen && !chromeVisible
                      ? 'pointer-events-none opacity-0'
                      : 'opacity-100',
                  ]"
                  @mouseenter="chromePinned = true"
                  @mouseleave="
                    chromePinned = false;
                    revealChrome();
                  "
                  @focusin="chromePinned = true"
                  @focusout="
                    chromePinned = false;
                    revealChrome();
                  "
                >
                  <h3
                    class="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
                  >
                    {{ t("capture.controls.title") }}
                  </h3>
                  <CameraControls />
                  <!-- The wheel is one of the three things reached for while staring at the frame. -->
                  <div
                    v-if="fullscreen && store.connected.wheel"
                    class="mt-4 border-t border-white/10 pt-3"
                  >
                    <h3
                      class="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
                    >
                      {{ t("capture.slots.title") }}
                    </h3>
                    <FilterSlots />
                  </div>
                </aside>

                <!-- Enter / leave, also fading with the rest of the chrome. -->
                <button
                  :class="[
                    fullscreen
                      ? 'absolute left-3 top-3 z-10 transition-opacity duration-300'
                      : 'absolute right-3 top-3',
                    fullscreen && !chromeVisible
                      ? 'pointer-events-none opacity-0'
                      : 'opacity-100',
                    'rounded-md bg-slate-900/80 px-2 py-1 text-xs text-slate-200 hover:bg-slate-900',
                  ]"
                  @click="fullscreen ? exitFullscreen() : enterFullscreen()"
                >
                  {{
                    fullscreen
                      ? t("capture.live.exitFullscreen")
                      : t("capture.live.fullscreen")
                  }}
                </button>
              </div>
            </section>
          </Teleport>

          <section :class="card">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.hist.title") }}
            </h2>
            <LiveHistogram :stats="store.liveStats" log />
          </section>

          <section :class="card" data-demo="capture-sequencer">
            <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
              <h2
                class="text-sm font-semibold text-slate-700 dark:text-slate-200"
              >
                {{ t("capture.run.title") }}
              </h2>
              <span
                v-if="panel"
                class="text-xs text-brand-600 dark:text-brand-300"
              >
                {{ t("capture.run.tile", { panel }) }}
              </span>
            </div>
            <SequenceRunner
              :path="capturePath"
              :object="objectName"
              :panel="panel"
              :mosaic-plan-id="mosaic.activePlan?.id"
              :image-scale-arcsec-px="imageScale"
              :ra-deg="tile?.ra_deg"
              :dec-deg="tile?.dec_deg"
            />
          </section>

          <section :class="card">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.sessions.title") }}
            </h2>
            <CaptureSessions />
          </section>
        </div>
      </template>

      <template #aside>
        <div class="space-y-4">
          <section :class="card" data-demo="capture-devices">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.device.title") }}
            </h2>
            <DevicePanel />
          </section>

          <section :class="card">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.mount.title") }}
            </h2>
            <MountPanel />
          </section>

          <section :class="card" data-demo="capture-polar">
            <PolarCameraPanel />
          </section>

          <section :class="card" data-demo="capture-filters">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.slots.title") }}
            </h2>
            <FilterSlots />
          </section>

          <section :class="card" data-demo="capture-focus">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.focus.title") }}
            </h2>
            <FocusMeter />
          </section>

          <section :class="card">
            <TrackingReport :session-id="store.progress?.session_id ?? null" />
          </section>

          <section :class="card">
            <h2
              class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200"
            >
              {{ t("capture.calib.title") }}
            </h2>
            <CalibrationWizard :path="capturePath" />
          </section>
        </div>
      </template>
    </TwoPane>
  </div>
</template>
