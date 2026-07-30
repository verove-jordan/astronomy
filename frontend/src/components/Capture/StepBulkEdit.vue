<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, btnPrimary, input } from "@/constants/styles";

// Change one value across several filters at once. Setting the exposure for L, R, G, B and Ha
// one field at a time is five chances to leave one at yesterday's value — and a single wrong row
// is a channel that will not stack with the others.
//
// Each field is opt-in: only the ticked ones are written, so this can raise the gain without
// touching anyone's frame counts. Zero is a legitimate value for gain, which is why the tick exists
// rather than treating "blank" as "no change".
const { t } = useI18n();
const props = defineProps<{ count: number }>();
const emit = defineEmits<{
  apply: [patch: { count?: number; exposure_us?: number; gain?: number }];
  close: [];
}>();

const useCount = ref(false);
const useExposure = ref(false);
const useGain = ref(false);
const countValue = ref(20);
const exposureSec = ref(120);
const gainValue = ref(139);

const anything = () => useCount.value || useExposure.value || useGain.value;

function apply() {
  if (!anything()) return;
  emit("apply", {
    count: useCount.value
      ? Math.max(1, Math.round(countValue.value))
      : undefined,
    exposure_us: useExposure.value
      ? Math.max(1, Math.round(exposureSec.value * 1e6))
      : undefined,
    gain: useGain.value ? Math.max(0, Math.round(gainValue.value)) : undefined,
  });
}
</script>

<template>
  <div
    class="fixed inset-0 z-40 flex items-center justify-center bg-black/40 p-4"
    @click.self="emit('close')"
  >
    <div
      class="w-full max-w-sm rounded-lg border border-slate-200 bg-white p-4 shadow-xl dark:border-slate-700 dark:bg-slate-800"
    >
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-100">
        {{ t("capture.bulk.title", { n: props.count }) }}
      </h3>
      <p class="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
        {{ t("capture.bulk.subtitle") }}
      </p>

      <div class="mt-3 space-y-2">
        <div class="flex items-center gap-2">
          <input
            id="bulk-count"
            v-model="useCount"
            type="checkbox"
            class="accent-brand-600"
          />
          <label
            for="bulk-count"
            class="w-28 text-xs text-slate-600 dark:text-slate-300"
            >{{ t("capture.bulk.frames") }}</label
          >
          <input
            v-model.number="countValue"
            type="number"
            min="1"
            :class="input"
            class="w-24"
            :disabled="!useCount"
          />
        </div>

        <div class="flex items-center gap-2">
          <input
            id="bulk-exp"
            v-model="useExposure"
            type="checkbox"
            class="accent-brand-600"
          />
          <label
            for="bulk-exp"
            class="w-28 text-xs text-slate-600 dark:text-slate-300"
            >{{ t("capture.bulk.exposure") }}</label
          >
          <input
            v-model.number="exposureSec"
            type="number"
            min="0"
            step="any"
            :class="input"
            class="w-24"
            :disabled="!useExposure"
          />
        </div>

        <div class="flex items-center gap-2">
          <input
            id="bulk-gain"
            v-model="useGain"
            type="checkbox"
            class="accent-brand-600"
          />
          <label
            for="bulk-gain"
            class="w-28 text-xs text-slate-600 dark:text-slate-300"
            >{{ t("capture.bulk.gain") }}</label
          >
          <input
            v-model.number="gainValue"
            type="number"
            min="0"
            :class="input"
            class="w-24"
            :disabled="!useGain"
          />
        </div>
      </div>

      <div class="mt-4 flex justify-end gap-2">
        <button
          :class="btnGhost"
          class="!px-3 !py-1 text-xs"
          @click="emit('close')"
        >
          {{ t("capture.bulk.cancel") }}
        </button>
        <button
          :class="btnPrimary"
          class="!px-3 !py-1 text-xs"
          :disabled="!anything()"
          @click="apply"
        >
          {{ t("capture.bulk.apply") }}
        </button>
      </div>
    </div>
  </div>
</template>
