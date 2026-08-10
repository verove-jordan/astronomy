<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import { useLogbookStore } from "@/stores/logbook";
import ConditionsChart from "@/components/Logbook/ConditionsChart.vue";
import FilterTally from "@/components/Logbook/FilterTally.vue";
import FrameTimeline from "@/components/Logbook/FrameTimeline.vue";
import SessionConditions from "@/components/Logbook/SessionConditions.vue";
import TrackingReport from "@/components/Capture/TrackingReport.vue";
import Spinner from "@/components/Common/Spinner.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import { btnGhost, card, checkbox, statusPill } from "@/constants/styles";
import { humanizeMs, formatTimestamp } from "@/utils/format";
import { tzForLocation } from "@/utils/tz";
import { integrationMs, nightKey, sessionDurationMs } from "@/utils/logbook";

// One night in full: what was shot, in what order, and under what sky.
const props = defineProps<{ id: string }>();
const { t } = useI18n();
const router = useRouter();
const store = useLogbookStore();

const showForecast = ref(false);

const sessionId = computed(() => Number(props.id));

watch(
  sessionId,
  (id, prev) => {
    if (!Number.isFinite(id)) return;
    // Drop the previous session's data first, or the page briefly renders one night's frames under
    // another's title while the new request is in flight.
    if (prev !== undefined && prev !== id) store.clearDetail();
    void store.loadSession(id);
    void store.loadConditions(id);
  },
  { immediate: true },
);

onBeforeUnmount(() => store.clearDetail());

const session = computed(() => store.detail?.session ?? null);
const stats = computed(() => store.detail?.frameStats ?? []);
const frames = computed(() => store.detail?.frames ?? []);
const conditions = computed(() => store.conditions?.conditions ?? []);
const summary = computed(() => store.conditions?.summary ?? null);

// The site is stored on the session, so a run shot on a trip reads in the hours it was really shot
// at rather than in the timezone of whatever machine is looking at it now.
const tz = computed(() => {
  const s = session.value;
  if (!s || (!s.site_lat && !s.site_lon)) {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  }
  return tzForLocation(s.site_lat, s.site_lon);
});

const startForecast = computed(
  () => store.conditions?.forecasts.find((f) => f.kind === "start") ?? null,
);

const totalIntegration = computed(() => humanizeMs(integrationMs(stats.value)));
const lightFrames = computed(() =>
  stats.value
    .filter((s) => s.frame_type.toLowerCase() === "light")
    .reduce((n, s) => n + s.frames, 0),
);
</script>

<template>
  <div class="space-y-6">
    <button :class="btnGhost" @click="router.push({ name: 'logbook' })">
      ← {{ t("logbook.title") }}
    </button>

    <Spinner v-if="store.detailLoading && !session">{{
      t("common.loading")
    }}</Spinner>
    <p v-else-if="!session" class="text-sm text-slate-400">
      {{ t("logbook.notFound") }}
    </p>

    <template v-else>
      <!-- Header: the identity of the night. -->
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="flex min-w-0 items-center gap-2">
            <h1 class="truncate text-2xl font-semibold">
              {{ session.object || t("logbook.untitled") }}
              <span
                v-if="session.panel"
                class="font-mono text-base text-slate-400"
              >
                {{ session.panel }}
              </span>
            </h1>
            <HelpButton />
          </div>
          <p class="truncate text-sm text-slate-500 dark:text-slate-400">
            {{ t("logbook.nightOf", { night: nightKey(session.started_at) }) }}
            · <span class="font-mono">{{ session.root }}</span>
          </p>
        </div>
        <span
          class="rounded px-2 py-1 text-xs"
          :class="statusPill[session.status] ?? statusPill.cancelled"
        >
          {{ t(`capture.status.${session.status}`) }}
        </span>
      </div>

      <!-- The four numbers that describe the run itself. -->
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <div :class="[card, '!p-3']">
          <p class="text-[11px] uppercase tracking-wide text-slate-500">
            {{ t("logbook.detail.started") }}
          </p>
          <p class="text-sm font-medium">
            {{ formatTimestamp(session.started_at) }}
          </p>
          <p class="text-[11px] text-slate-400">
            {{
              session.ended_at
                ? formatTimestamp(session.ended_at)
                : t("logbook.detail.running")
            }}
          </p>
        </div>
        <div :class="[card, '!p-3']">
          <p class="text-[11px] uppercase tracking-wide text-slate-500">
            {{ t("logbook.detail.duration") }}
          </p>
          <p class="text-lg font-semibold tabular-nums">
            {{ humanizeMs(sessionDurationMs(session)) }}
          </p>
        </div>
        <div :class="[card, '!p-3']">
          <p class="text-[11px] uppercase tracking-wide text-slate-500">
            {{ t("logbook.detail.lights") }}
          </p>
          <p class="text-lg font-semibold tabular-nums">
            {{ lightFrames }}
            <span class="text-sm font-normal text-slate-400"
              >/ {{ session.total_frames }}</span
            >
          </p>
        </div>
        <!-- Open-shutter time, not wall clock: this is what predicts how deep the stack goes. -->
        <div :class="[card, '!p-3']">
          <p class="text-[11px] uppercase tracking-wide text-slate-500">
            {{ t("logbook.detail.integration") }}
          </p>
          <p class="text-lg font-semibold tabular-nums">
            {{ totalIntegration }}
          </p>
        </div>
      </div>

      <!-- What was shot. -->
      <section :class="card" data-demo="logbook-tally">
        <h2 class="mb-3 text-lg font-medium">{{ t("logbook.tally.title") }}</h2>
        <FilterTally v-if="stats.length" :stats="stats" />
        <p v-else class="text-sm text-slate-400">
          {{ t("logbook.tally.empty") }}
        </p>
      </section>

      <!-- In what order. -->
      <section :class="card">
        <h2 class="mb-1 text-lg font-medium">
          {{ t("logbook.timeline.title") }}
        </h2>
        <p class="mb-3 text-xs text-slate-500 dark:text-slate-400">
          {{ t("logbook.timeline.blurb") }}
        </p>
        <FrameTimeline :frames="frames" :tz="tz" />
      </section>

      <!-- Under what sky. -->
      <section :class="card">
        <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
          <h2 class="text-lg font-medium">
            {{ t("logbook.conditions.title") }}
          </h2>
          <label
            v-if="startForecast"
            class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400"
          >
            <input v-model="showForecast" type="checkbox" :class="checkbox" />
            {{ t("logbook.conditions.showForecast") }}
          </label>
        </div>

        <Spinner v-if="store.conditionsLoading && !conditions.length">
          {{ t("common.loading") }}
        </Spinner>

        <!-- A session from before the logbook shipped has no record and can never get one: the
             weather providers only reach about a day back. Say so instead of drawing an empty chart. -->
        <p v-else-if="!conditions.length" class="text-sm text-slate-400">
          {{ store.conditions?.message || t("logbook.conditions.none") }}
        </p>

        <div v-else class="space-y-5">
          <SessionConditions v-if="summary" :summary="summary" />
          <ConditionsChart
            :rows="conditions"
            :forecast="startForecast"
            :show-forecast="showForecast"
            :tz="tz"
          />
        </div>
      </section>

      <!-- How the mount tracked, when it was measured. -->
      <section :class="card">
        <h2 class="mb-3 text-lg font-medium">
          {{ t("capture.tracking.title") }}
        </h2>
        <TrackingReport :session-id="session.id" />
      </section>
    </template>
  </div>
</template>
