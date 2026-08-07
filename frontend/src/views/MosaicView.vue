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

// Object names are compared the way a human reads them: "M 42", "m42" and "M42" are one object.
function sameObject(a: string, b: string) {
  const key = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, "");
  return !!a && !!b && key(a) === key(b);
}

onMounted(async () => {
  const object =
    typeof route.query.object === "string" ? route.query.object : "";
  const planId = Number(route.query.plan) || store.activePlanId;
  if (object) {
    store.seedFromObject(object);
    // Seeding is planning, not capturing: it fills the draft and saves nothing. The active plan id
    // survives reloads, so without this the Capture tab still belongs to whatever was captured last
    // — and picking a target that is overhead in Tonight would land on a Capture tab offering to
    // slew at an unrelated object that set hours ago, refused for being below the horizon. Deselect
    // only on a genuine change of object, so returning to a mosaic already in progress still resumes.
    await store.listPlans();
    const active = store.plans.find((p) => p.id === store.activePlanId);
    if (active && !sameObject(active.object_name, object)) store.deselectPlan();
    return;
  }
  void store.listPlans();
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
