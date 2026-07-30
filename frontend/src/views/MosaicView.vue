<script setup lang="ts">
import { computed, onMounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import TabBar from "@/components/Common/TabBar.vue";
import MosaicPlanner from "@/components/Mosaic/MosaicPlanner.vue";
import CaptureAssistant from "@/components/Mosaic/CaptureAssistant.vue";
import { useMosaicStore } from "@/stores/mosaic";

// The Mosaic hub: Plan (object → overlapping tile grid → saved plan) and Capture (step through the
// tiles at the scope). Tab + plan id live in the query string so the capture tab is a bookmarkable
// phone URL (/mosaic?tab=capture&plan=3); ?object= seeds the planner from Tonight.
const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const store = useMosaicStore();

const tab = computed<"plan" | "capture">(() =>
  route.query.tab === "capture" ? "capture" : "plan",
);
function selectTab(key: string) {
  void router.replace({ query: { ...route.query, tab: key } });
}

onMounted(async () => {
  void store.listPlans();
  const object =
    typeof route.query.object === "string" ? route.query.object : "";
  const planId = Number(route.query.plan) || store.activePlanId;
  if (object) {
    store.seedFromObject(object);
    return;
  }
  if (planId) {
    try {
      const plan = await store.loadPlan(planId, true);
      store.draftFromPlan(plan);
    } catch {
      // stale/removed plan id — the planner just opens empty
    }
  }
});
</script>

<template>
  <div class="mx-auto max-w-7xl space-y-4 px-4 py-6">
    <Teleport to="#page-tabs">
      <TabBar
        :tabs="[
          { key: 'plan', label: t('mosaic.tabs.plan') },
          { key: 'capture', label: t('mosaic.tabs.capture') },
        ]"
        :active="tab"
        @select="selectTab"
      />
    </Teleport>
    <div>
      <h1 class="text-2xl font-semibold">{{ t("mosaic.title") }}</h1>
      <p class="text-sm text-slate-400">{{ t("mosaic.subtitle") }}</p>
    </div>
    <MosaicPlanner v-if="tab === 'plan'" />
    <CaptureAssistant v-else />
  </div>
</template>
