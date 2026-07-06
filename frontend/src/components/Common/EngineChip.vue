<script setup lang="ts">
// Small mono chip naming the engine build that produced a run (run.json / result `engine`), amber —
// with an explanatory tooltip — when it differs from the build currently serving (/api/health), so a
// "the fix didn't work" image is recognisable as a stale-build product at a glance. Hidden for
// un-stamped ("dev") runs.
import { computed, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useJobsStore } from "@/stores/jobs";
import Pill from "@/components/Common/Pill.vue";

const props = defineProps<{ engine?: string }>();
const { t } = useI18n();
const jobsStore = useJobsStore();
onMounted(() => void jobsStore.fetchHealth());

// Runs stamp buildinfo.String() — "version" or "version (built_at)" — while /api/health exposes the
// version alone; compare version parts only so two builds of one tag don't false-flag on timestamps.
const version = computed(() => (props.engine ?? "").split(" (")[0]);
const visible = computed(() => !!version.value && version.value !== "dev");
const stale = computed(
  () =>
    visible.value &&
    !!jobsStore.engineVersion &&
    jobsStore.engineVersion !== "dev" &&
    version.value !== jobsStore.engineVersion,
);
const cls = computed(() =>
  stale.value
    ? "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
    : "bg-slate-100 text-slate-500 dark:bg-slate-700/60 dark:text-slate-400",
);
const title = computed(() =>
  stale.value ? t("runs.engineStale") : `${t("runs.engine")} ${props.engine}`,
);
</script>

<template>
  <Pill v-if="visible" :color-class="cls" :title="title">
    <span class="font-mono">{{ version }}</span>
  </Pill>
</template>
