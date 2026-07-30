<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import Pill from "@/components/Common/Pill.vue";
import { card } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";
import type { MosaicPlanRow } from "@/types";

// Saved plans: load into the planner, jump to capture, duplicate, rename, delete. A short personal
// list — rows with inline actions (the tile table is the page's GenericTable surface).
const { t } = useI18n();
const router = useRouter();
const store = useMosaicStore();

void store.listPlans();

const confirmDeleteId = ref<number | null>(null);

async function load(plan: MosaicPlanRow) {
  const full = await store.loadPlan(plan.id, true);
  store.draftFromPlan(full);
}

function capture(plan: MosaicPlanRow) {
  void router.push({
    name: "mosaic",
    query: { tab: "capture", plan: String(plan.id) },
  });
  void store.loadPlan(plan.id, true);
}

async function duplicate(plan: MosaicPlanRow) {
  const name = window.prompt(t("mosaic.plans.duplicateName"), `${plan.name} +`);
  if (!name) return;
  await store.duplicatePlan(plan.id, name.trim());
}

async function rename(plan: MosaicPlanRow) {
  const name = window.prompt(t("mosaic.plans.renamePrompt"), plan.name);
  if (!name || name.trim() === plan.name) return;
  await store.renamePlan(plan.id, name.trim());
}

async function remove(plan: MosaicPlanRow) {
  if (confirmDeleteId.value !== plan.id) {
    confirmDeleteId.value = plan.id;
    return;
  }
  confirmDeleteId.value = null;
  await store.deletePlan(plan.id);
}
</script>

<template>
  <div :class="card">
    <h2 class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200">
      {{ t("mosaic.plans.title") }}
    </h2>
    <p v-if="!store.plans.length" class="text-sm text-slate-400">
      {{ t("mosaic.plans.empty") }}
    </p>
    <ul v-else class="space-y-2">
      <li
        v-for="plan in store.plans"
        :key="plan.id"
        class="rounded-md border border-slate-200 p-2 dark:border-slate-700"
        :class="
          plan.id === store.activePlan?.id
            ? 'ring-1 ring-brand-500 dark:ring-brand-400'
            : ''
        "
      >
        <div class="flex items-center justify-between gap-2">
          <button
            class="min-w-0 truncate text-left text-sm font-medium text-slate-800 hover:text-brand-600 dark:text-slate-100 dark:hover:text-brand-300"
            :title="t('mosaic.plans.load')"
            @click="load(plan)"
          >
            {{ plan.name }}
          </button>
          <Pill
            color-class="bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-200"
            >{{ store.planProgress(plan).captured }}/{{
              plan.tiles.length
            }}</Pill
          >
        </div>
        <p class="text-xs text-slate-400">
          {{ plan.object_name || "—" }} · {{ plan.grid.cols }}×{{
            plan.grid.rows
          }}
        </p>
        <div class="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs">
          <button
            class="text-brand-600 hover:underline dark:text-brand-300"
            @click="capture(plan)"
          >
            {{ t("mosaic.plans.capture") }}
          </button>
          <button
            class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
            @click="duplicate(plan)"
          >
            {{ t("mosaic.plans.duplicate") }}
          </button>
          <button
            class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
            @click="rename(plan)"
          >
            {{ t("mosaic.plans.rename") }}
          </button>
          <button class="text-danger-500 hover:underline" @click="remove(plan)">
            {{
              confirmDeleteId === plan.id
                ? t("mosaic.plans.confirmDelete")
                : t("mosaic.plans.delete")
            }}
          </button>
        </div>
      </li>
    </ul>
  </div>
</template>
