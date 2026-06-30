<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import TabBar from "@/components/Common/TabBar.vue";

// The consolidated "Processing" hub: one page, five tabs (Import / Live / Tasks / Runs / Library), each
// a child route. The shared TabBar is teleported into the App shell's sticky band; the child view renders
// below it. Job detail (route name "job") folds into the Tasks tab.
const route = useRoute();
const router = useRouter();
const { t } = useI18n();

const keyToName: Record<string, string> = {
  import: "import",
  live: "livestack",
  tasks: "jobs",
  runs: "runs",
  library: "library",
};
const nameToKey: Record<string, string> = {
  import: "import",
  livestack: "live",
  jobs: "tasks",
  job: "tasks", // job detail lives under the Tasks tab
  runs: "runs",
  library: "library",
};

const tabs = computed(() => [
  { key: "import", label: t("processing.tabs.import") },
  { key: "live", label: t("processing.tabs.live") },
  { key: "tasks", label: t("processing.tabs.tasks") },
  { key: "runs", label: t("processing.tabs.runs") },
  { key: "library", label: t("processing.tabs.library") },
]);

const active = computed(() => nameToKey[String(route.name)] ?? "import");

function select(key: string) {
  const name = keyToName[key];
  if (name && name !== route.name) router.push({ name });
}
</script>

<template>
  <Teleport to="#page-tabs">
    <TabBar :tabs="tabs" :active="active" @select="select" />
  </Teleport>
  <router-view />
</template>
