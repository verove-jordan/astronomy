<script setup lang="ts">
import { useI18n } from "vue-i18n";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import PolarSchema from "@/components/Polar/PolarSchema.vue";

// PolarTutorial walks through polar-scope setup and real-night use, tailored to a SkyWatcher polar
// scope on a Celestron Advanced VX. Each step pairs an inline schematic with prose + practical tips.
const { t, tm } = useI18n();

const steps = [1, 2, 3, 4, 5, 6];
const stepTitle = (n: number) =>
  `${n}. ${t(`tonight.polar.tutorial.s${n}.title`)}`;
const stepBody = (n: number) => t(`tonight.polar.tutorial.s${n}.body`);
const stepKey = (n: number) => `astrostack.polar.step.${n}`;
function tips(n: number): string[] {
  const v = tm(`tonight.polar.tutorial.s${n}.tips`);
  return Array.isArray(v) ? (v as string[]) : [];
}
</script>

<template>
  <div class="space-y-3">
    <div>
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("tonight.polar.tutorial.title") }}
      </h3>
      <p class="text-xs text-slate-400">
        {{ t("tonight.polar.tutorial.intro") }}
      </p>
    </div>
    <CollapsibleCard
      v-for="n in steps"
      :key="n"
      :title="stepTitle(n)"
      :storage-key="stepKey(n)"
    >
      <div class="grid gap-3 sm:grid-cols-[220px_1fr] sm:items-start">
        <PolarSchema :step="n" />
        <div>
          <p class="text-sm text-slate-600 dark:text-slate-300">
            {{ stepBody(n) }}
          </p>
          <ul
            class="mt-2 list-disc space-y-1 pl-5 text-xs text-slate-500 dark:text-slate-400"
          >
            <li v-for="(tip, i) in tips(n)" :key="i">{{ tip }}</li>
          </ul>
        </div>
      </div>
    </CollapsibleCard>
  </div>
</template>
