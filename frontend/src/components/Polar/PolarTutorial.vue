<script setup lang="ts">
import { useI18n } from "vue-i18n";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import PolarSchema from "@/components/Polar/PolarSchema.vue";

// PolarTutorial walks through polar-scope setup and real-night use, tailored to a SkyWatcher polar
// scope on a Celestron Advanced VX. Each step pairs an inline schematic with prose + practical tips.
// The whole tutorial sits in one outer card, COLLAPSED by default (fresh storage key — the per-step
// keys persist "open" from before): it lives at the bottom of the GoTo alignment page as reference
// material, not part of the nightly flow.
const { t, tm } = useI18n();

const steps = [1, 2, 3, 4, 5, 6];
const stepTitle = (n: number) => `${n}. ${t(`polar.tutorial.s${n}.title`)}`;
const stepBody = (n: number) => t(`polar.tutorial.s${n}.body`);
const stepKey = (n: number) => `astrostack.polar.step.${n}`;
function tips(n: number): string[] {
  const v = tm(`polar.tutorial.s${n}.tips`);
  return Array.isArray(v) ? (v as string[]) : [];
}
</script>

<template>
  <CollapsibleCard
    :title="t('polar.tutorial.title')"
    storage-key="astrostack.goto.polartutorial"
    :default-open="false"
  >
    <div class="space-y-3">
      <p class="text-xs text-slate-400">
        {{ t("polar.tutorial.intro") }}
      </p>
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
  </CollapsibleCard>
</template>
