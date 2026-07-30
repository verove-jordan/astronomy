<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  STORAGE_CLASSES,
  INSTANT_CLASSES,
  type StorageClassMeta,
} from "@/constants/storageClasses";

// A radio list of S3 storage classes with an inline explanation of each (the "explain each class so the
// user can select" ask). Reused by the explorer's "change storage class" action (all classes) and the
// connection default-class field (instantOnly — a write default must stay readable). v-model = class id.
const props = withDefaults(
  defineProps<{
    modelValue: string;
    instantOnly?: boolean;
  }>(),
  { instantOnly: false },
);
const emit = defineEmits<{ "update:modelValue": [string] }>();
const { t } = useI18n();

const classes = computed<StorageClassMeta[]>(() =>
  props.instantOnly ? INSTANT_CLASSES : STORAGE_CLASSES,
);
</script>

<template>
  <ul class="space-y-1.5">
    <li v-for="c in classes" :key="c.id">
      <button
        type="button"
        class="w-full rounded-lg border p-2.5 text-left transition-colors"
        :class="
          modelValue === c.id
            ? 'border-brand-500 bg-brand-50 dark:border-brand-400 dark:bg-brand-950/40'
            : 'border-slate-200 hover:border-slate-300 dark:border-slate-700 dark:hover:border-slate-600'
        "
        @click="emit('update:modelValue', c.id)"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-semibold">{{
            t(`storageClass.classes.${c.id}.label`)
          }}</span>
          <span
            class="rounded px-1.5 py-0.5 text-[10px] font-medium uppercase"
            :class="
              c.family === 'archived'
                ? 'bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-300'
                : 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300'
            "
            >{{ t(`storageClass.family.${c.family}`) }}</span
          >
          <span
            v-if="c.needsThaw"
            class="text-[11px] font-medium text-amber-600 dark:text-amber-400"
            >❄ {{ t("storageClass.needsThaw") }}</span
          >
        </div>
        <p class="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
          {{ t(`storageClass.classes.${c.id}.blurb`) }}
        </p>
        <p
          v-if="c.minDurationDays > 0"
          class="mt-0.5 text-[11px] text-slate-400 dark:text-slate-500"
        >
          {{ t("storageClass.minDuration", { days: c.minDurationDays }) }}
        </p>
      </button>
    </li>
  </ul>
</template>
