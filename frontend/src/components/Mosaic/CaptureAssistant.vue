<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import CapturePlanPanel from "@/components/Mosaic/CapturePlanPanel.vue";
import TileSequence from "@/components/Mosaic/TileSequence.vue";
import { card, input } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";

// The Capture tab: pick a saved plan, then walk its tiles (single column, oversized touch targets —
// this runs on a phone at the scope in the dark). Statuses live server-side, so a desktop-made plan
// resumes on the phone exactly where it stands.
const { t } = useI18n();
const router = useRouter();
const store = useMosaicStore();

const loadError = ref("");

onMounted(async () => {
  await store.listPlans();
  if (!store.activePlan && store.activePlanId) {
    try {
      await store.loadPlan(store.activePlanId, true);
    } catch {
      // plan removed elsewhere — the selector below is the recovery path
    }
  }
});

async function select(idRaw: string) {
  loadError.value = "";
  const id = Number(idRaw);
  if (!id) return;
  try {
    await store.loadPlan(id, true);
    void router.replace({ query: { tab: "capture", plan: idRaw } });
  } catch (e) {
    loadError.value = String(e instanceof Error ? e.message : e);
  }
}
</script>

<template>
  <div class="mx-auto max-w-xl space-y-4">
    <div :class="card">
      <label
        class="text-sm font-semibold text-slate-700 dark:text-slate-200"
        for="mosaic-plan-select"
        >{{ t("mosaic.capture.selectPlan") }}</label
      >
      <select
        id="mosaic-plan-select"
        :value="store.activePlan?.id ?? ''"
        :class="input"
        class="mt-1"
        @change="select(($event.target as HTMLSelectElement).value)"
      >
        <option value="" disabled>
          {{ t("mosaic.capture.selectPlaceholder") }}
        </option>
        <option v-for="plan in store.plans" :key="plan.id" :value="plan.id">
          {{ plan.name }} · {{ store.planProgress(plan).captured }}/{{
            plan.tiles.length
          }}
        </option>
      </select>
      <p v-if="!store.plans.length" class="mt-2 text-sm text-slate-400">
        {{ t("mosaic.capture.noPlans") }}
      </p>
      <p v-if="loadError" class="mt-1 text-xs text-danger-500">
        {{ loadError }}
      </p>
    </div>

    <CapturePlanPanel />

    <TileSequence v-if="store.activePlan" />
  </div>
</template>
