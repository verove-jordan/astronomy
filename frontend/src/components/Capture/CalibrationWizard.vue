<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, btnPrimary, input } from "@/constants/styles";
import { apiPost } from "@/services/api";
import { useCaptureStore } from "@/stores/capture";
import type { CaptureSequence, CaptureStep } from "@/types";

// The calibration wizard.
//
// Calibration frames only cancel what they match, and the matching rules are where most libraries
// go wrong. So the settings are taken from the lights rather than retyped, the plan is built on the
// server next to the sequencer, and each step carries the reason it exists.
// The calibration frames land beside the lights they match, so the path comes from the page rather
// than being chosen again here.
const props = defineProps<{ path?: string }>();
const { t } = useI18n();
const store = useCaptureStore();

type Kind = "bias" | "dark" | "flat" | "darkflat";

interface Plan {
  sequence: CaptureSequence;
  total_frames: number;
  estimated_seconds: number;
  notes: string[];
  warnings?: string[];
}

const kinds = ref<Record<Kind, boolean>>({
  bias: true,
  dark: true,
  flat: true,
  darkflat: false,
});
// These describe the LIGHTS the calibration is being planned for, so they track the sequence
// defaults. A dark library shot at a different gain does not calibrate frames shot at gain 0 — the
// matcher would simply refuse it, which reads as "no masters found" rather than as a mismatch.
const exposureSec = ref(60);
const gain = ref(0);
const offset = ref(21);
const flatExposureUs = ref(0);

// Flats are per-filter, so this list decides which flats get shot at all. Seeding it from the
// WHEEL's own slot labels is what makes a narrowband rig work out of the box: the old hardcoded
// "L,R,G,B" meant Ha/OIII/SII flats were only ever shot by someone who remembered to retype the
// field, and a missing flat is not obvious until the master is built. Falls back to the broadband
// default when no wheel is connected, and stops seeding as soon as the user edits it.
const filters = ref("L,R,G,B");
const filtersEdited = ref(false);
const wheelFilters = computed<string[]>(() =>
  (store.wheel?.wheel?.names ?? []).map((n) => n.trim()).filter(Boolean),
);
watch(
  wheelFilters,
  (list) => {
    if (filtersEdited.value || !list.length) return;
    filters.value = list.join(",");
  },
  { immediate: true },
);

const plan = ref<Plan | null>(null);
const error = ref("");
const busy = ref(false);
const measuring = ref(false);
const flatMessage = ref("");

const selectedKinds = computed(() =>
  (Object.keys(kinds.value) as Kind[]).filter((k) => kinds.value[k]),
);
const needsFlatExposure = computed(
  () => (kinds.value.flat || kinds.value.darkflat) && flatExposureUs.value <= 0,
);

// Flat exposure has to be measured: it depends on the light panel, the filter and the f-ratio, and
// no default could be right for all three.
async function measureFlat() {
  measuring.value = true;
  error.value = "";
  try {
    const res = await apiPost<{
      exposure_us: number;
      median_adu: number;
      converged: boolean;
      message?: string;
    }>("/api/device/flat-exposure", { gain: gain.value, offset: offset.value });
    flatExposureUs.value = res.exposure_us;
    flatMessage.value = res.converged
      ? t("capture.calib.flatMeasured", {
          seconds: (res.exposure_us / 1e6).toFixed(2),
          adu: Math.round(res.median_adu),
        })
      : (res.message ?? "");
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    measuring.value = false;
  }
}

async function buildPlan() {
  busy.value = true;
  error.value = "";
  plan.value = null;
  try {
    plan.value = await apiPost<Plan>("/api/capture/calibration/plan", {
      kinds: selectedKinds.value,
      flat_exposure_us: flatExposureUs.value,
      lights: {
        exposure_us: Math.round(exposureSec.value * 1e6),
        gain: gain.value,
        offset: offset.value,
        bin: 1,
        filters: filters.value
          .split(",")
          .map((f) => f.trim())
          .filter(Boolean),
        has_temp: true,
      },
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

async function runPlan() {
  if (!plan.value) return;
  error.value = "";
  try {
    await store.startSequence({
      sequence: plan.value.sequence,
      path: props.path ?? "",
      object: "calibration",
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

const duration = (sec: number) => {
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h} h ${m} min` : `${m} min`;
};
const expo = (us: number) =>
  us >= 1e6 ? `${us / 1e6} s` : us >= 1000 ? `${us / 1000} ms` : `${us} µs`;
</script>

<template>
  <div class="space-y-3">
    <p class="text-[11px] text-slate-500 dark:text-slate-400">
      {{ t("capture.calib.blurb") }}
    </p>

    <!-- Which frames -->
    <div class="flex flex-wrap gap-3 text-xs">
      <label
        v-for="k in ['bias', 'dark', 'flat', 'darkflat'] as Kind[]"
        :key="k"
        class="flex items-center gap-1 text-slate-600 dark:text-slate-300"
      >
        <input v-model="kinds[k]" type="checkbox" class="accent-brand-600" />
        {{ t(`capture.calib.kind_${k}`) }}
      </label>
    </div>

    <!-- The lights these must match -->
    <div class="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
      <label class="text-slate-500 dark:text-slate-400">
        {{ t("capture.calib.lightExposure") }}
        <input
          v-model.number="exposureSec"
          type="number"
          min="0.001"
          step="1"
          :class="input"
          class="mt-0.5"
        />
      </label>
      <label class="text-slate-500 dark:text-slate-400">
        {{ t("capture.calib.gain") }}
        <input
          v-model.number="gain"
          type="number"
          min="0"
          :class="input"
          class="mt-0.5"
        />
      </label>
      <label class="text-slate-500 dark:text-slate-400">
        {{ t("capture.calib.offset") }}
        <input
          v-model.number="offset"
          type="number"
          min="0"
          :class="input"
          class="mt-0.5"
        />
      </label>
      <label class="text-slate-500 dark:text-slate-400">
        {{ t("capture.calib.filters") }}
        <input
          v-model="filters"
          type="text"
          :class="input"
          class="mt-0.5"
          @input="filtersEdited = true"
        />
      </label>
    </div>

    <!-- Flat exposure must be measured, never guessed -->
    <div v-if="kinds.flat || kinds.darkflat" class="space-y-1">
      <div class="flex flex-wrap items-center gap-2">
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          :disabled="measuring || !store.connected.camera"
          @click="measureFlat"
        >
          {{
            measuring
              ? t("capture.calib.measuring")
              : t("capture.calib.measureFlat")
          }}
        </button>
        <span
          v-if="flatExposureUs > 0"
          class="font-mono text-xs text-slate-500"
        >
          {{ expo(flatExposureUs) }}
        </span>
      </div>
      <p
        v-if="flatMessage"
        class="text-[11px] text-slate-500 dark:text-slate-400"
      >
        {{ flatMessage }}
      </p>
      <p
        v-if="needsFlatExposure"
        class="text-[11px] text-amber-600 dark:text-amber-400"
      >
        {{ t("capture.calib.flatNeeded") }}
      </p>
    </div>

    <div class="flex gap-2">
      <button
        :class="btnGhost"
        class="!px-2 !py-1 text-xs"
        :disabled="busy || !selectedKinds.length"
        @click="buildPlan"
      >
        {{ t("capture.calib.build") }}
      </button>
      <button
        v-if="plan"
        :class="btnPrimary"
        class="!px-2 !py-1 text-xs"
        :disabled="!store.connected.camera"
        @click="runPlan"
      >
        {{ t("capture.calib.run") }}
      </button>
    </div>

    <!-- The plan, with its reasoning -->
    <div v-if="plan" class="space-y-2">
      <p class="text-xs text-slate-600 dark:text-slate-300">
        {{
          t("capture.calib.summary", {
            frames: plan.total_frames,
            time: duration(plan.estimated_seconds),
          })
        }}
      </p>

      <ul
        class="space-y-0.5 font-mono text-[11px] text-slate-500 dark:text-slate-400"
      >
        <li v-for="(s, i) in plan.sequence.steps as CaptureStep[]" :key="i">
          {{ s.count }}× {{ t(`capture.calib.kind_${s.type}`) }}
          <template v-if="s.filter">· {{ s.filter }}</template>
          · {{ expo(s.exposure_us) }} · gain {{ s.gain }}
        </li>
      </ul>

      <ul class="space-y-1 text-[11px] text-slate-500 dark:text-slate-400">
        <li v-for="n in plan.notes" :key="n">· {{ n }}</li>
      </ul>

      <ul
        v-if="plan.warnings?.length"
        class="space-y-1 text-[11px] text-amber-600 dark:text-amber-400"
      >
        <li v-for="wn in plan.warnings" :key="wn">⚠ {{ wn }}</li>
      </ul>
    </div>

    <p v-if="error" class="text-xs text-danger-500">{{ error }}</p>
  </div>
</template>
