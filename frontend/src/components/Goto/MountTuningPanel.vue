<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import CollapsibleCard from "@/components/Common/CollapsibleCard.vue";
import Pill from "@/components/Common/Pill.vue";
import { useGotoStore } from "@/stores/goto";
import {
  MOUNT_TUNING,
  MOUNT_MODELS,
  defaultModelForProfile,
  type IssueSeverity,
  type FixEffort,
} from "@/constants/mountTuning";
import { input } from "@/constants/styles";

// MountTuningPanel: "what's wrong with this mount & how to compensate" — a reference card mirroring
// PolarTutorial (outer CollapsibleCard collapsed by default, one nested card per fix, tm() step
// arrays). The model defaults from the alignment profile above until the user picks one explicitly;
// an explicit pick is persisted and wins from then on.
const { t, tm } = useI18n();
const store = useGotoStore();

const MOUNT_KEY = "astrostack.goto.mount";
const savedModel = ref<string | null>(localStorage.getItem(MOUNT_KEY));
const model = computed({
  get: () => {
    const saved = savedModel.value;
    if (saved && MOUNT_TUNING[saved]) return saved;
    return defaultModelForProfile(store.params.profile ?? "eq-generic");
  },
  set: (v: string) => {
    savedModel.value = v;
    try {
      localStorage.setItem(MOUNT_KEY, v);
    } catch {
      // ignore quota / private-mode errors
    }
  },
});
const tuning = computed(
  () => MOUNT_TUNING[model.value] ?? MOUNT_TUNING.generic,
);

// Complete literal class strings (JIT-safe), reusing the shared Pill primitive.
const severityPill: Record<IssueSeverity, string> = {
  high: "bg-red-100 text-red-800 dark:bg-red-900/40 dark:text-red-300",
  medium:
    "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
};
const effortPill: Record<FixEffort, string> = {
  free: "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  paid: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-300",
};

const fixTitle = (n: number, id: string) =>
  `${n}. ${t(`goto.tuning.models.${model.value}.fixes.${id}.title`)}`;
const fixKey = (id: string) =>
  `astrostack.goto.mounttuning.${model.value}.${id}`;
function steps(id: string): string[] {
  const v = tm(`goto.tuning.models.${model.value}.fixes.${id}.steps`);
  return Array.isArray(v) ? (v as string[]) : [];
}
</script>

<template>
  <CollapsibleCard
    :title="t('goto.tuning.title')"
    storage-key="astrostack.goto.mounttuning"
    :default-open="false"
  >
    <div class="space-y-4">
      <p class="text-xs text-slate-400">{{ t("goto.tuning.intro") }}</p>

      <div class="max-w-sm">
        <label class="mb-1 block text-xs font-medium text-slate-400">{{
          t("goto.tuning.mountLabel")
        }}</label>
        <select v-model="model" :class="input">
          <option v-for="m in MOUNT_MODELS" :key="m" :value="m">
            {{ t(`goto.tuning.models.${m}.name`) }}
          </option>
        </select>
      </div>

      <!-- What's wrong with this mount -->
      <div>
        <h4
          class="text-[11px] font-semibold uppercase tracking-wide text-slate-400"
        >
          {{ t("goto.tuning.summaryTitle") }}
        </h4>
        <p class="mt-1 text-sm text-slate-600 dark:text-slate-300">
          {{ t(`goto.tuning.models.${model}.summary`) }}
        </p>
      </div>

      <!-- Issue cards: symptom → why -->
      <div>
        <h4
          class="text-[11px] font-semibold uppercase tracking-wide text-slate-400"
        >
          {{ t("goto.tuning.issuesTitle") }}
        </h4>
        <div class="mt-2 grid gap-3 sm:grid-cols-2">
          <div
            v-for="issue in tuning.issues"
            :key="issue.id"
            class="rounded-lg border border-slate-200 p-3 dark:border-slate-700"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span
                class="text-sm font-semibold text-slate-800 dark:text-slate-100"
                >{{
                  t(`goto.tuning.models.${model}.issues.${issue.id}.label`)
                }}</span
              >
              <Pill :color-class="severityPill[issue.severity]">{{
                t(`goto.tuning.severity.${issue.severity}`)
              }}</Pill>
            </div>
            <p class="mt-1 text-sm text-slate-600 dark:text-slate-300">
              {{ t(`goto.tuning.models.${model}.issues.${issue.id}.symptom`) }}
            </p>
            <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
              {{ t(`goto.tuning.models.${model}.issues.${issue.id}.why`) }}
            </p>
          </div>
        </div>
      </div>

      <!-- Fix cards: numbered steps + effort badge -->
      <div>
        <h4
          class="text-[11px] font-semibold uppercase tracking-wide text-slate-400"
        >
          {{ t("goto.tuning.fixesTitle") }}
        </h4>
        <div class="mt-2 space-y-3">
          <CollapsibleCard
            v-for="(fix, i) in tuning.fixes"
            :key="fix.id"
            :title="fixTitle(i + 1, fix.id)"
            :storage-key="fixKey(fix.id)"
          >
            <div class="mb-2 flex flex-wrap items-center gap-2">
              <Pill :color-class="effortPill[fix.effort]">{{
                t(`goto.tuning.effort.${fix.effort}`)
              }}</Pill>
              <span v-if="fix.minutes" class="text-xs text-slate-400">{{
                t("goto.tuning.minutes", { m: fix.minutes })
              }}</span>
            </div>
            <ol
              class="list-decimal space-y-1 pl-5 text-sm text-slate-600 dark:text-slate-300"
            >
              <li v-for="(s, j) in steps(fix.id)" :key="j">{{ s }}</li>
            </ol>
          </CollapsibleCard>
        </div>
      </div>
    </div>
  </CollapsibleCard>
</template>
