<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";

// What the "Dither" number actually does. The field deserves an explanation because its name gives
// no clue and the obvious guess — that it processes the image on the way to disk — is wrong and
// alarming. Nothing touches the frames: it moves the TELESCOPE between them.
const { t } = useI18n();
defineProps<{ blocked?: string }>();
const open = ref(false);
</script>

<template>
  <div class="text-[11px]">
    <button
      class="text-brand-600 hover:underline dark:text-brand-300"
      :aria-expanded="open"
      @click="open = !open"
    >
      {{ open ? t("capture.dither.hide") : t("capture.dither.what") }}
    </button>

    <p v-if="blocked" class="mt-1 text-amber-600 dark:text-amber-400">
      {{ blocked }}
    </p>

    <div
      v-if="open"
      class="mt-1 space-y-1.5 rounded-md border border-slate-200 p-2 text-slate-600 dark:border-slate-700 dark:text-slate-300"
    >
      <p class="font-medium text-slate-700 dark:text-slate-200">
        {{ t("capture.dither.headline") }}
      </p>
      <p>{{ t("capture.dither.notProcessing") }}</p>
      <p>{{ t("capture.dither.why") }}</p>
      <p>{{ t("capture.dither.value") }}</p>
      <ul class="ml-4 list-disc space-y-0.5">
        <li>{{ t("capture.dither.value0") }}</li>
        <li>{{ t("capture.dither.value1") }}</li>
        <li>{{ t("capture.dither.value5") }}</li>
        <li>{{ t("capture.dither.valueBig") }}</li>
      </ul>
      <p>{{ t("capture.dither.cost") }}</p>
      <p>{{ t("capture.dither.how") }}</p>
      <p class="text-slate-500 dark:text-slate-400">
        {{ t("capture.dither.safety") }}
      </p>
    </div>
  </div>
</template>
