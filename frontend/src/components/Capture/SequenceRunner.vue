<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import ProgressBar from "@/components/Common/ProgressBar.vue";
import DestinationPicker from "@/components/Capture/DestinationPicker.vue";
import DitherHelp from "@/components/Capture/DitherHelp.vue";
import StepBulkEdit from "@/components/Capture/StepBulkEdit.vue";
import DurationInput from "@/components/Capture/DurationInput.vue";
import { btnGhost, btnPrimary, input } from "@/constants/styles";
import { useCountdown } from "@/composables/useCountdown";
import { useCaptureStore } from "@/stores/capture";
import type { CaptureStep } from "@/types";

import { nextUnusedFilter } from "@/constants/filters";

// The auto-run: declare the night once — which filters, how many frames, what exposure — and let it
// run. This is the part of ASICAP that has to be babysat; here the plan is a document, the progress
// is streamed, and pausing waits for the current frame rather than throwing it away.
const { t } = useI18n();
const store = useCaptureStore();

// The filters actually fitted to the connected wheel, in slot order. Empty when no wheel is
// connected — the row then falls back to a free-text field so a plan is still writable offline.
const wheelFilters = computed<string[]>(() =>
  (store.wheel?.wheel?.names ?? []).map((n) => n.trim()).filter(Boolean),
);

const props = defineProps<{
  path?: string;
  object?: string;
  panel?: string;
  mosaicPlanId?: number;
  imageScaleArcsecPx?: number;
  raDeg?: number;
  decDeg?: number;
}>();

// defaultStep is what a new filter row starts as.
//
// Gain 0, not the ASI1600's unity gain of 139: gain is analogue amplification applied before the
// converter, so anything above zero trades full-well depth — and therefore bright-star headroom —
// for read noise that longer exposures make irrelevant anyway. At 60 s subs the sky background
// already swamps the read noise, so the amplification buys nothing and costs highlights.
function defaultStep(): CaptureStep {
  return {
    filter: "L",
    count: 20,
    exposure_us: 60_000_000,
    gain: 0,
    offset: 50,
    bin: 1,
    type: "light",
    dither_n: 5,
  };
}

const steps = ref<CaptureStep[]>([defaultStep()]);
const interleave = ref(false);
const rootPath = ref(props.path ?? "");
const objectName = ref(props.object ?? "");
const saveName = ref("");
const startError = ref("");

// Bulk editing: which rows a change applies to.
const selectedRows = ref<Set<number>>(new Set());
const bulkOpen = ref(false);

watch(
  () => props.path,
  (p) => {
    if (p) rootPath.value = p;
  },
);
watch(
  () => props.object,
  (o) => {
    if (o) objectName.value = o;
  },
);

const totalFrames = computed(() =>
  steps.value.reduce((n, s) => n + Math.max(0, s.count || 0), 0),
);
const totalSeconds = computed(() =>
  steps.value.reduce(
    (n, s) => n + (Math.max(0, s.count || 0) * (s.exposure_us || 0)) / 1e6,
    0,
  ),
);

// A new filter inherits the previous row's settings. Within one night the exposure, gain and frame
// count are nearly always the same across channels — retyping them for every filter is pure friction,
// and a row that silently reverts to defaults is a night shot at the wrong gain.
function addStep() {
  const last = steps.value[steps.value.length - 1];
  steps.value.push({
    ...(last ? { ...last } : defaultStep()),
    filter: nextUnusedFilter(steps.value.map((s) => s.filter)),
  });
}

// Assigning a filter to a wheel slot adds a channel to the auto-run.
//
// The two were disconnected: you would name slot 3 "OIII", then have to add an OIII row by hand. The
// wheel's assignment IS the list of filters this rig can shoot tonight, so it seeds the sequence — new
// rows inherit the previous row's frames/exposure/gain, which is what you almost always want.
//
// Filters already seeded are remembered, so deleting a row STAYS deleted. Re-adding it on the next
// wheel poll would make the row impossible to remove.
const seededFilters = ref(new Set<string>());

watch(
  () => store.wheel?.wheel?.names?.join("|") ?? "",
  () => {
    const assigned = (store.wheel?.wheel?.names ?? []).filter(
      (n) => n.trim() !== "",
    );
    if (!assigned.length) return;
    const present = new Set(steps.value.map((x) => x.filter));

    // An untouched single default row is a placeholder, not a choice: replace it rather than leaving
    // a stray "L" above the filters actually fitted.
    if (
      steps.value.length === 1 &&
      !seededFilters.value.size &&
      steps.value[0].filter === defaultStep().filter
    ) {
      const base = { ...steps.value[0] };
      steps.value = assigned.map((f) => ({ ...base, filter: f }));
      assigned.forEach((f) => seededFilters.value.add(f));
      return;
    }

    for (const f of assigned) {
      if (present.has(f) || seededFilters.value.has(f)) continue;
      const last = steps.value[steps.value.length - 1];
      steps.value.push({ ...(last ? { ...last } : defaultStep()), filter: f });
      seededFilters.value.add(f);
    }
  },
  { immediate: true },
);

function removeStep(i: number) {
  // Marked as seeded on the way out, so the wheel watcher does not put it straight back.
  const gone = steps.value[i]?.filter;
  if (gone) seededFilters.value.add(gone);
  steps.value.splice(i, 1);
  selectedRows.value.delete(i);
  // Re-index the selection so it keeps pointing at the same rows.
  selectedRows.value = new Set(
    [...selectedRows.value].map((idx) => (idx > i ? idx - 1 : idx)),
  );
}

const allSelected = computed({
  get: () =>
    steps.value.length > 0 && selectedRows.value.size === steps.value.length,
  set: (on: boolean) => {
    selectedRows.value = on
      ? new Set(steps.value.map((_, i) => i))
      : new Set<number>();
  },
});

function toggleRow(i: number, on: boolean) {
  const next = new Set(selectedRows.value);
  if (on) next.add(i);
  else next.delete(i);
  selectedRows.value = next;
}

// applyBulk writes only the fields the modal actually asked to change, to the checked rows.
function applyBulk(patch: {
  count?: number;
  exposure_us?: number;
  gain?: number;
}) {
  const targets = selectedRows.value.size
    ? [...selectedRows.value]
    : steps.value.map((_, i) => i);
  for (const i of targets) {
    const step = steps.value[i];
    if (!step) continue;
    if (patch.count !== undefined) step.count = patch.count;
    if (patch.exposure_us !== undefined) step.exposure_us = patch.exposure_us;
    if (patch.gain !== undefined) step.gain = patch.gain;
  }
  bulkOpen.value = false;
}

// Measuring the mount costs a plate solve per frame, which competes with a livestack running
// alongside — so it is the user's choice, not a default.
const measureTracking = ref(false);

// Time left on the sub being exposed right now, ticking. The overall ETA answers "when is the night
// done"; this answers "when does THIS frame land", which is the one you watch while framing or
// checking that a filter change took.
const currentSub = useCountdown(
  () => store.progress?.exposure_ends,
  () => store.progress?.exposure_us,
);

async function start() {
  startError.value = "";
  try {
    await store.startSequence({
      sequence: { steps: steps.value, interleave: interleave.value },
      path: rootPath.value,
      object: objectName.value,
      panel: props.panel,
      mosaic_plan_id: props.mosaicPlanId,
      dither_radius_px: 10,
      image_scale_arcsec_px: props.imageScaleArcsecPx,
      ra_deg: props.raDeg,
      dec_deg: props.decDeg,
      measure_tracking: measureTracking.value,
    });
  } catch (e) {
    startError.value = e instanceof Error ? e.message : String(e);
  }
}

async function save() {
  if (!saveName.value.trim()) return;
  await store.saveSequence(saveName.value.trim(), {
    steps: steps.value,
    interleave: interleave.value,
  });
  saveName.value = "";
}

function load(id: number) {
  const found = store.sequences.find((s) => s.id === id);
  if (!found) return;
  steps.value = found.payload.steps.map((s) => ({ ...s }));
  interleave.value = !!found.payload.interleave;
  selectedRows.value = new Set();
}

const percent = computed(() => {
  const p = store.progress;
  if (!p || !p.total_frames) return 0;
  return (100 * p.frame_index) / p.total_frames;
});

const eta = computed(() => {
  const s = store.progress?.eta_seconds ?? 0;
  if (!s) return "";
  const h = Math.floor(s / 3600);
  const m = Math.round((s % 3600) / 60);
  return h > 0 ? `${h} h ${m} min` : `${m} min`;
});

// Dithering moves the MOUNT, so it needs one connected and a known image scale. Saying so up front
// beats discovering mid-run that every dither was skipped.
const ditherRequested = computed(() =>
  steps.value.some((s) => (s.dither_n ?? 0) > 0),
);
const ditherBlocked = computed(() => {
  if (!ditherRequested.value) return "";
  if (!store.connected.mount) return t("capture.dither.needsMount");
  if (!props.imageScaleArcsecPx) return t("capture.dither.needsScale");
  return "";
});
</script>

<template>
  <div class="space-y-3">
    <!-- Live progress -->
    <div
      v-if="store.progress && store.progress.status !== 'idle'"
      class="space-y-1"
    >
      <ProgressBar :percent="percent" />
      <div
        class="flex flex-wrap items-center gap-2 text-xs text-slate-500 dark:text-slate-400"
      >
        <span class="font-medium">{{
          t(`capture.status.${store.progress.status}`)
        }}</span>
        <span
          >{{ store.progress.frame_index }}/{{ store.progress.total_frames }}
          {{ t("capture.run.frames") }}</span
        >
        <span v-if="store.progress.current_filter" class="font-mono">{{
          store.progress.current_filter
        }}</span>
        <!-- Seconds left on the sub in flight, so a 300 s exposure is visibly progressing. -->
        <span
          v-if="currentSub.seconds.value"
          class="font-mono tabular-nums text-brand-600 dark:text-brand-300"
          :title="t('capture.run.subRemaining')"
          >⏱ {{ currentSub.label.value }}</span
        >
        <span v-if="eta">· {{ t("capture.run.eta", { eta }) }}</span>
        <span
          v-if="store.progress.message"
          class="text-amber-600 dark:text-amber-400"
          >{{ store.progress.message }}</span
        >
      </div>
      <p v-if="store.progress.error" class="text-xs text-danger-500">
        {{ store.progress.error }}
      </p>
      <div class="flex flex-wrap gap-2">
        <button
          v-if="store.progress.status === 'running'"
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="store.pause()"
        >
          {{ t("capture.run.pause") }}
        </button>
        <button
          v-if="store.progress.status === 'paused'"
          :class="btnPrimary"
          class="!px-2 !py-1 text-xs"
          @click="store.resume()"
        >
          {{ t("capture.run.resume") }}
        </button>
        <button
          v-if="store.running"
          :class="btnGhost"
          class="!px-2 !py-1 text-xs !text-danger-500"
          @click="store.abort()"
        >
          {{ t("capture.run.abort") }}
        </button>
      </div>
    </div>

    <!-- Plan editor -->
    <div class="space-y-2">
      <div
        v-if="steps.length > 1"
        class="flex flex-wrap items-center gap-2 text-xs"
      >
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="allSelected"
            type="checkbox"
            class="accent-brand-600"
          />
          {{ t("capture.run.selectAll") }}
        </label>
        <button :class="btnGhost" class="!px-2 !py-1" @click="bulkOpen = true">
          {{
            selectedRows.size
              ? t("capture.run.editSelected", { n: selectedRows.size })
              : t("capture.run.editAll")
          }}
        </button>
      </div>

      <div
        v-for="(step, i) in steps"
        :key="i"
        class="flex flex-wrap items-end gap-2 rounded-md border p-2"
        :class="
          selectedRows.has(i)
            ? 'border-brand-500/60 bg-brand-50/40 dark:bg-brand-900/10'
            : 'border-slate-200 dark:border-slate-700'
        "
      >
        <input
          v-if="steps.length > 1"
          type="checkbox"
          class="mb-2 accent-brand-600"
          :checked="selectedRows.has(i)"
          :aria-label="t('capture.run.selectRow')"
          @change="toggleRow(i, ($event.target as HTMLInputElement).checked)"
        />
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.run.filter") }}
          <!-- Offered from the WHEEL's own slot labels: a name the wheel does not carry fails the
               step mid-night, and a typo is the easiest way to lose a session. Falls back to free
               text when no wheel is connected, so a plan can still be written at the desk. -->
          <select
            v-if="wheelFilters.length"
            v-model="step.filter"
            :class="input"
            class="w-20"
          >
            <option v-for="f in wheelFilters" :key="f" :value="f">
              {{ f }}
            </option>
            <option
              v-if="!wheelFilters.includes(step.filter)"
              :value="step.filter"
            >
              {{ step.filter || "—" }}
            </option>
          </select>
          <input v-else v-model="step.filter" :class="input" class="w-16" />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.run.count") }}
          <input
            v-model.number="step.count"
            type="number"
            min="1"
            :class="input"
            class="w-16"
          />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.run.exposure") }}
          <DurationInput
            v-model="step.exposure_us"
            :input-class="`${input} w-20`"
            :select-class="`${input} w-16`"
          />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.run.gain") }}
          <input
            v-model.number="step.gain"
            type="number"
            min="0"
            :class="input"
            class="w-16"
          />
        </label>
        <label class="text-xs text-slate-500 dark:text-slate-400"
          >{{ t("capture.run.dither") }}
          <input
            v-model.number="step.dither_n"
            type="number"
            min="0"
            :class="input"
            class="w-14"
          />
        </label>
        <button
          class="pb-2 text-xs text-danger-500 hover:underline"
          @click="removeStep(i)"
        >
          {{ t("capture.run.remove") }}
        </button>
      </div>

      <div class="flex flex-wrap items-center gap-2 text-xs">
        <button :class="btnGhost" class="!px-2 !py-1" @click="addStep">
          {{ t("capture.run.addStep") }}
        </button>
        <label
          class="flex items-center gap-1 text-slate-500 dark:text-slate-400"
        >
          <input
            v-model="interleave"
            type="checkbox"
            class="accent-brand-600"
          />
          {{ t("capture.run.interleave") }}
        </label>
        <span class="text-slate-400">{{
          t("capture.run.total", {
            frames: totalFrames,
            hours: (totalSeconds / 3600).toFixed(1),
          })
        }}</span>
      </div>
      <p class="text-[11px] text-slate-400">
        {{ t("capture.run.interleaveHint") }}
      </p>

      <DitherHelp :blocked="ditherBlocked" />

      <label
        class="flex items-start gap-1.5 text-xs text-slate-600 dark:text-slate-300"
      >
        <input
          v-model="measureTracking"
          type="checkbox"
          class="mt-0.5 accent-brand-600"
        />
        <span>
          {{ t("capture.tracking.measure") }}
          <span class="block text-[11px] text-slate-400">
            {{ t("capture.tracking.measureHint") }}
          </span>
        </span>
      </label>
    </div>

    <!-- Destination + launch -->
    <div class="space-y-2 border-t border-slate-200 pt-3 dark:border-slate-700">
      <label class="block text-xs text-slate-500 dark:text-slate-400"
        >{{ t("capture.run.object") }}
        <input v-model="objectName" :class="input" />
      </label>
      <DestinationPicker v-model="rootPath" />
      <div class="flex flex-wrap items-center gap-2">
        <button
          :class="btnPrimary"
          :disabled="store.running || !rootPath || !totalFrames"
          @click="start"
        >
          {{ t("capture.run.start") }}
        </button>
        <input
          v-model="saveName"
          :class="input"
          class="w-40"
          :placeholder="t('capture.run.savePlaceholder')"
        />
        <button :class="btnGhost" class="!px-2 !py-1 text-xs" @click="save">
          {{ t("capture.run.save") }}
        </button>
        <select
          v-if="store.sequences.length"
          :class="input"
          class="w-40"
          @change="load(Number(($event.target as HTMLSelectElement).value))"
        >
          <option value="">{{ t("capture.run.loadSaved") }}</option>
          <option v-for="s in store.sequences" :key="s.id" :value="s.id">
            {{ s.name }}
          </option>
        </select>
      </div>
      <p v-if="startError" class="text-xs text-danger-500">{{ startError }}</p>
    </div>

    <StepBulkEdit
      v-if="bulkOpen"
      :count="selectedRows.size || steps.length"
      @apply="applyBulk"
      @close="bulkOpen = false"
    />
  </div>
</template>
