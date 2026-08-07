<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, btnPrimary, scoreTierPill } from "@/constants/styles";
import type { ScoreTier } from "@/constants/styles";
import { usePolarCamStore } from "@/stores/polarCam";
import { useCaptureStore } from "@/stores/capture";
import Pill from "@/components/Common/Pill.vue";
import StatGrid from "@/components/Common/StatGrid.vue";
import Spinner from "@/components/Common/Spinner.vue";

// Polar alignment, measured through the telescope.
//
// The panel is a procedure, not a form: point somewhere, take a frame, turn the axis by hand, take
// another, and be told what to do about it. The numbers it ends with are correct and nearly unusable
// in the dark — so the answer that matters is the marker the live view draws, and the last step is
// simply "drive it into the crosshairs".

const { t } = useI18n();
const polar = usePolarCamStore();
const capture = useCaptureStore();

onMounted(async () => {
  polar.watch();
  try {
    await polar.refreshStatus();
  } catch {
    // The panel renders from the stream, so a failed first read costs nothing worth a message.
  }
});
onBeforeUnmount(() => polar.unwatch());

const cameraReady = computed(() => Boolean(capture.camera?.connected));
const busy = computed(() => Boolean(polar.state?.busy) || polar.starting);

// The four steps, so the panel shows the whole procedure up front rather than one button at a time.
// Knowing there are three more presses to come is the difference between following a process and
// being led somewhere.
type StepKey = "prepare" | "measure" | "result" | "adjust";
const steps: StepKey[] = ["prepare", "measure", "result", "adjust"];

const activeStep = computed<StepKey>(() => {
  switch (polar.phase) {
    case "measuring":
      return "measure";
    case "solved":
      return "result";
    case "adjusting":
      return "adjust";
    default:
      return "prepare";
  }
});

function stepState(key: StepKey): "done" | "active" | "todo" {
  const order = steps.indexOf(key);
  const current = steps.indexOf(activeStep.value);
  if (order < current) return "done";
  return order === current ? "active" : "todo";
}

const STEP_DOT: Record<string, string> = {
  done: "bg-success-500",
  active: "bg-brand-500",
  todo: "bg-slate-300 dark:bg-slate-600",
};
const STEP_LABEL: Record<string, string> = {
  done: "text-slate-500 dark:text-slate-400",
  active: "font-semibold text-slate-800 dark:text-slate-100",
  todo: "text-slate-400 dark:text-slate-500",
};

// The alignment quality bands are the same four the sky-target scoring uses, so they get the same
// chip colours rather than a second palette that would drift from it.
const qualityPill = (quality: string): string =>
  scoreTierPill[quality as ScoreTier] ?? scoreTierPill.poor;

// arcmin renders an angle the way somebody at a mount reads it: whole arcminutes and seconds, because
// "0.2043°" is not a quantity anyone can turn a bolt by.
function arcmin(deg: number | undefined): string {
  if (deg === undefined) return "—";
  const total = Math.abs(deg) * 60;
  const m = Math.floor(total);
  const s = Math.round((total - m) * 60);
  return s === 60 ? `${m + 1}′00″` : `${m}′${String(s).padStart(2, "0")}″`;
}

// How far the pole is from the middle of the frame, in the unit a human can act on: degrees while you
// are still hunting for it, arcminutes once it is close.
const poleDistance = computed(() => {
  const p = polar.pole;
  if (!p) return "";
  const off = p.pole.offset_arcmin;
  const angle = off >= 60 ? `${(off / 60).toFixed(2)}°` : arcmin(off / 60);
  return t("capture.polar.finder.distance", { angle });
});

const correction = computed(() => polar.state?.correction);
const live = computed(() => polar.state?.live);

// The instruction is assembled from the backend's direction WORD, never from the sign of a number:
// which way "left" is depends on the hemisphere, and that decision belongs where the geometry is.
const altInstruction = computed(() => {
  const c = correction.value;
  if (!c || c.alt_move === "ok") return "";
  return t(`capture.polar.move.${c.alt_move}`, {
    angle: arcmin(c.alt_error_deg),
  });
});
const azInstruction = computed(() => {
  const c = correction.value;
  if (!c || c.az_move === "ok") return "";
  // The KNOB angle, not the sky angle: the azimuth adjuster turns through 1/cos(latitude) more than
  // the error it removes, and quoting the smaller number makes every user undershoot.
  return t(`capture.polar.move.${c.az_move}`, { angle: arcmin(c.az_knob_deg) });
});

const resultStats = computed(() => {
  const c = correction.value;
  const a = polar.state?.axis;
  if (!c || !a) return [];
  return [
    {
      label: t("capture.polar.stats.total"),
      value: arcmin(c.total_arcmin / 60),
    },
    {
      label: t("capture.polar.stats.altitude"),
      value: arcmin(c.alt_error_deg),
    },
    { label: t("capture.polar.stats.azimuth"), value: arcmin(c.az_knob_deg) },
    {
      label: t("capture.polar.stats.precision"),
      value: `±${arcmin(a.sigma_arcsec / 3600)}`,
      hint: t("capture.polar.stats.precisionHint"),
    },
    { label: t("capture.polar.stats.arc"), value: `${a.arc_deg.toFixed(0)}°` },
    { label: t("capture.polar.stats.frames"), value: a.samples },
  ];
});
</script>

<template>
  <div class="space-y-3">
    <div class="flex items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("capture.polar.title") }}
      </h2>
      <div class="flex items-center gap-1">
        <Pill v-if="polar.isRough" :color-class="scoreTierPill.fair">
          {{ t("capture.polar.rough.badge") }}
        </Pill>
        <Pill v-if="live?.quality" :color-class="qualityPill(live.quality)">
          {{ t(`capture.polar.quality.${live.quality}`) }}
        </Pill>
      </div>
    </div>
    <p class="text-xs text-slate-500 dark:text-slate-400">
      {{ t("capture.polar.subtitle") }}
    </p>

    <!-- The whole procedure, always visible: four steps, one highlighted. -->
    <ol class="space-y-1">
      <li
        v-for="(key, i) in steps"
        :key="key"
        class="flex items-center gap-2 text-xs"
      >
        <span
          class="h-2 w-2 shrink-0 rounded-full"
          :class="STEP_DOT[stepState(key)]"
          aria-hidden="true"
        />
        <span :class="STEP_LABEL[stepState(key)]">
          {{ i + 1 }}. {{ t(`capture.polar.steps.${key}`) }}
        </span>
      </li>
    </ol>

    <!-- 1. Prepare -->
    <div v-if="activeStep === 'prepare'" class="space-y-2">
      <ul
        class="list-disc space-y-1 pl-4 text-xs text-slate-600 dark:text-slate-300"
      >
        <li>{{ t("capture.polar.prepare.camera") }}</li>
        <li>{{ t("capture.polar.prepare.focus") }}</li>
        <li>{{ t("capture.polar.prepare.field") }}</li>
        <li>{{ t("capture.polar.prepare.clutch") }}</li>
      </ul>
      <div class="flex flex-wrap gap-2">
        <button
          :class="btnPrimary"
          :disabled="!cameraReady || busy"
          @click="polar.start()"
        >
          {{ t("capture.polar.actions.start") }}
        </button>
        <button
          :class="btnGhost"
          :disabled="!cameraReady || busy"
          @click="polar.rough()"
        >
          {{ t("capture.polar.actions.rough") }}
        </button>
      </div>
      <p class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("capture.polar.prepare.orPole") }}
      </p>
      <p v-if="!cameraReady" class="text-xs text-warning-600">
        {{ t("capture.polar.prepare.needCamera") }}
      </p>
    </div>

    <!-- 2. Measure -->
    <div v-else-if="activeStep === 'measure'" class="space-y-2">
      <p class="text-sm text-slate-700 dark:text-slate-200">
        {{
          t("capture.polar.measure.progress", {
            step: polar.state?.step ?? 0,
            total: polar.state?.points ?? 0,
          })
        }}
      </p>
      <p class="text-xs text-slate-600 dark:text-slate-300">
        {{
          t("capture.polar.measure.turn", {
            degrees: polar.state?.step_arc_deg ?? 20,
          })
        }}
      </p>
      <div class="flex gap-2">
        <button :class="btnPrimary" :disabled="busy" @click="polar.next()">
          {{ t("capture.polar.actions.next") }}
        </button>
        <button :class="btnGhost" :disabled="busy" @click="polar.stop()">
          {{ t("capture.polar.actions.cancel") }}
        </button>
      </div>
    </div>

    <!-- 3. Result -->
    <div v-else-if="activeStep === 'result'" class="space-y-2">
      <p
        v-if="polar.isRough"
        class="rounded-md bg-amber-50 px-2 py-1 text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-300"
      >
        {{ t("capture.polar.rough.caveat") }}
      </p>
      <StatGrid :items="resultStats" :cols="3" />
      <div class="space-y-1 text-sm">
        <p v-if="altInstruction" class="text-slate-800 dark:text-slate-100">
          {{ altInstruction }}
        </p>
        <p v-if="azInstruction" class="text-slate-800 dark:text-slate-100">
          {{ azInstruction }}
        </p>
        <p
          v-if="!altInstruction && !azInstruction"
          class="text-success-600 dark:text-success-500"
        >
          {{ t("capture.polar.result.alreadyGood") }}
        </p>
      </div>
      <div class="flex gap-2">
        <button :class="btnPrimary" :disabled="busy" @click="polar.adjust()">
          {{ t("capture.polar.actions.adjust") }}
        </button>
        <button :class="btnGhost" :disabled="busy" @click="polar.stop()">
          {{ t("capture.polar.actions.done") }}
        </button>
      </div>
    </div>

    <!-- 4. Adjust -->
    <div v-else class="space-y-2">
      <p class="text-xs text-slate-600 dark:text-slate-300">
        {{ t("capture.polar.adjust.instruction") }}
      </p>
      <StatGrid
        :items="[
          {
            label: t('capture.polar.stats.remaining'),
            value: arcmin((live?.remaining_arcmin ?? 0) / 60),
          },
          {
            label: t('capture.polar.stats.markerOffset'),
            value: arcmin((live?.target?.offset_arcmin ?? 0) / 60),
          },
        ]"
        :cols="2"
      />
      <p
        v-if="live?.target?.off_frame"
        class="text-xs text-warning-600 dark:text-warning-500"
      >
        {{ t("capture.polar.adjust.offFrame") }}
      </p>
      <p
        v-if="live?.suspect"
        class="text-xs text-danger-600 dark:text-danger-500"
        role="status"
      >
        {{ t("capture.polar.adjust.suspect") }}
      </p>
      <div class="flex gap-2">
        <button :class="btnPrimary" :disabled="busy" @click="polar.start()">
          {{ t("capture.polar.actions.remeasure") }}
        </button>
        <button :class="btnGhost" @click="polar.stop()">
          {{ t("capture.polar.actions.done") }}
        </button>
      </div>
    </div>

    <p
      v-if="poleDistance"
      class="text-xs text-slate-600 dark:text-slate-300"
      role="status"
    >
      {{ poleDistance }}
      <span v-if="polar.pole?.star_visible">
        {{ t("capture.polar.finder.star", { name: polar.pole.star_name }) }}
      </span>
    </p>
    <p
      v-if="polar.pole && polar.pole.pole.off_frame"
      class="text-xs text-warning-600 dark:text-warning-500"
    >
      {{ t("capture.polar.finder.offFrame") }}
    </p>

    <Spinner v-if="busy">
      <span class="text-xs">{{ t("capture.polar.busy") }}</span>
    </Spinner>

    <!-- Anything the fit wanted to say about how much to trust itself. -->
    <ul
      v-if="polar.warnings.length"
      class="space-y-1 text-xs text-warning-600 dark:text-warning-500"
    >
      <li v-for="code in polar.warnings" :key="code">
        {{ t(`capture.polar.warnings.${code}`) }}
      </li>
    </ul>

    <p
      v-if="polar.error || polar.state?.error"
      class="text-xs text-danger-600 dark:text-danger-500"
      role="status"
    >
      {{ polar.error || polar.state?.error }}
    </p>
  </div>
</template>
