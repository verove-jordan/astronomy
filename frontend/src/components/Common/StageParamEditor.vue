<script setup lang="ts">
// A modal to edit one processing stage's parameters and re-run the pipeline from that stage. It shows
// only the knobs that belong to the stage's re-entry tier (composite / linear prep / re-stack — see
// constants/knobs.ts), prefilled from the run's current params; "Re-run from here" emits just the knobs
// the user actually changed, so the backend re-enters at the cheapest stage reflecting the edit. The
// parent (JobView) owns the store call + navigation; this component only collects the edit.
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  card,
  input,
  checkbox,
  btnPrimary,
  btnGhost,
} from "@/constants/styles";
import {
  knobsForStage,
  tierGroupForStage,
  TIER_HINT_KEY,
} from "@/constants/knobs";
import type { JobParams } from "@/types";

const props = defineProps<{
  stage: string;
  params?: JobParams;
  busy?: boolean;
  error?: string;
}>();
const emit = defineEmits<{
  submit: [payload: { stage: string; params: Record<string, unknown> }];
  close: [];
}>();
const { t } = useI18n();

const knobs = computed(() => knobsForStage(props.stage));
const tierHintKey = computed(
  () => TIER_HINT_KEY[tierGroupForStage(props.stage)],
);

// The knob values the user edits, seeded from the run's overrides (params.params) or each knob's display
// default. `initial` snapshots that seed so we send ONLY the knobs the user actually changed — an
// untouched knob is never sent, so the backend preserves its true (checkpoint) baseline.
type KnobValue = number | boolean | string;
function seed(): Record<string, KnobValue> {
  const over = (props.params?.params ?? {}) as Record<string, unknown>;
  const out: Record<string, KnobValue> = {};
  for (const k of knobs.value) {
    const want =
      k.kind === "toggle" ? "boolean" : k.kind === "select" ? "string" : "number";
    out[k.key] =
      typeof over[k.key] === want ? (over[k.key] as KnobValue) : k.def;
  }
  return out;
}
const values = ref<Record<string, KnobValue>>(seed());
const initial = seed();

const changedKeys = computed(() =>
  knobs.value
    .filter((k) => values.value[k.key] !== initial[k.key])
    .map((k) => k.key),
);

function stageLabel(stage: string): string {
  const key = `stagePreviews.stages.${stage}`;
  const label = t(key);
  return label === key ? stage : label;
}

function submit() {
  const patch: Record<string, unknown> = {};
  for (const key of changedKeys.value) patch[key] = values.value[key];
  emit("submit", { stage: props.stage, params: patch });
}
</script>

<template>
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
    @click.self="emit('close')"
  >
    <div :class="[card, 'w-full max-w-lg']">
      <h2 class="text-lg font-medium">
        {{ t("rerun.title", { stage: stageLabel(stage) }) }}
      </h2>
      <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
        {{ t(tierHintKey) }}
      </p>

      <div class="mt-4 space-y-3">
        <div
          v-for="k in knobs"
          :key="k.key"
          class="flex items-center justify-between gap-3"
        >
          <label :for="`knob-${k.key}`" class="text-sm">
            {{ t(k.labelKey) }}
            <span v-if="k.kind === 'number'" class="ml-1 text-xs text-slate-400"
              >({{ k.min }}–{{ k.max }})</span
            >
          </label>
          <input
            v-if="k.kind === 'number'"
            :id="`knob-${k.key}`"
            v-model.number="values[k.key]"
            type="number"
            :min="k.min"
            :max="k.max"
            :step="k.step"
            :class="[input, 'w-28']"
          />
          <select
            v-else-if="k.kind === 'select'"
            :id="`knob-${k.key}`"
            v-model="values[k.key]"
            :class="[input, 'w-40']"
          >
            <option v-for="opt in k.options" :key="opt" :value="opt">
              {{ t(`${k.labelKey}Options.${opt}`) }}
            </option>
          </select>
          <input
            v-else
            :id="`knob-${k.key}`"
            v-model="values[k.key]"
            type="checkbox"
            :class="checkbox"
          />
        </div>
      </div>

      <p v-if="error" class="mt-3 text-sm text-danger-600">{{ error }}</p>

      <div class="mt-5 flex items-center justify-between">
        <span class="text-xs text-slate-400">
          {{
            changedKeys.length
              ? t("rerun.changedCount", { n: changedKeys.length })
              : t("rerun.noChange")
          }}
        </span>
        <div class="flex gap-2">
          <button :class="btnGhost" :disabled="busy" @click="emit('close')">
            {{ t("rerun.cancel") }}
          </button>
          <button :class="btnPrimary" :disabled="busy" @click="submit">
            {{ busy ? t("rerun.running") : t("rerun.run") }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
