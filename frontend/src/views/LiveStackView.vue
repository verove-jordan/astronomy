<script setup lang="ts">
import { ref, computed, onMounted } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import { useBrowseStore } from "@/stores/browse";
import { useJobsStore } from "@/stores/jobs";
import FileBrowser from "@/components/Common/FileBrowser.vue";
import HelpButton from "@/components/Common/HelpButton.vue";
import { btnPrimary, btnGhost, card, input } from "@/constants/styles";

// The live-stacking start form. It only collects the source + exposure, creates a "livestack" job and
// hands off to JobView, which already streams the live preview, sub counter, logs and the Stop button.
const router = useRouter();
const { t } = useI18n();
const browseStore = useBrowseStore();
const jobsStore = useJobsStore();

const sourceKind = ref<"local" | "s3">("local");
const watchPath = ref("");
const rootPath = ref("");
const bucket = ref("");
const prefix = ref("");
const exposureSec = ref(60);
const launching = ref(false);

onMounted(async () => {
  await browseStore.browse();
  rootPath.value = browseStore.path;
});

async function openDir(path: string) {
  await browseStore.browse(path);
}
// The watched folder may be empty when the session starts, so selection does not require an inspect.
function selectDir(path: string) {
  watchPath.value = path;
}
function useCurrentDir() {
  watchPath.value = browseStore.path;
}

const tabClass = (kind: "local" | "s3") =>
  kind === sourceKind.value
    ? "rounded-md px-3 py-2 text-sm font-medium bg-brand-600 text-white"
    : "rounded-md px-3 py-2 text-sm font-medium text-slate-300 hover:bg-slate-700";

const canStart = computed(() => {
  if (launching.value || exposureSec.value <= 0) return false;
  return sourceKind.value === "local" ? !!watchPath.value : !!bucket.value;
});

async function start() {
  launching.value = true;
  try {
    const isS3 = sourceKind.value === "s3";
    const path = isS3
      ? `s3://${bucket.value}/${prefix.value}`
      : watchPath.value;
    const id = await jobsStore.create(path, "livestack", "image", {
      live: {
        sourceKind: sourceKind.value,
        bucket: isS3 ? bucket.value : undefined,
        prefix: isS3 ? prefix.value : undefined,
        exposureSec: exposureSec.value,
      },
    });
    router.push({ name: "job", params: { id: String(id) } });
  } finally {
    launching.value = false;
  }
}
</script>

<template>
  <div class="space-y-6">
    <div>
      <div class="flex items-center gap-2">
        <h1 class="text-2xl font-semibold">{{ t("livestack.title") }}</h1>
        <HelpButton />
      </div>
      <p class="text-sm text-slate-500 dark:text-slate-400">
        {{ t("livestack.hint") }}
      </p>
    </div>

    <div :class="card">
      <div class="mb-4 flex gap-2">
        <button :class="tabClass('local')" @click="sourceKind = 'local'">
          {{ t("livestack.local") }}
        </button>
        <button :class="tabClass('s3')" @click="sourceKind = 's3'">
          {{ t("livestack.s3") }}
        </button>
      </div>

      <div v-if="sourceKind === 'local'" class="space-y-3" data-demo="live-source">
        <p class="text-xs text-slate-400">{{ t("livestack.pickFolder") }}</p>
        <FileBrowser
          :path="browseStore.path"
          :root="rootPath"
          :entries="browseStore.entries"
          :loading="browseStore.loading"
          :multi-select="false"
          :error="browseStore.error"
          :fetch-children="browseStore.listDir"
          @navigate="openDir"
          @inspect="(paths) => selectDir(paths[0])"
        />
        <div class="flex flex-wrap items-center gap-3">
          <button :class="btnGhost" @click="useCurrentDir">
            {{ t("livestack.useCurrent") }}
          </button>
          <span v-if="watchPath" class="truncate text-sm text-slate-400">
            {{ t("livestack.watching") }}: {{ watchPath }}
          </span>
        </div>
      </div>

      <div v-else class="grid gap-3 sm:grid-cols-2">
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("livestack.bucket")
          }}</span>
          <input v-model="bucket" :class="input" placeholder="my-captures" />
        </label>
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("livestack.prefix")
          }}</span>
          <input
            v-model="prefix"
            :class="input"
            placeholder="M101/2026-06-28/"
          />
        </label>
        <p class="text-xs text-slate-400 sm:col-span-2">
          {{ t("livestack.s3hint") }}
        </p>
      </div>
    </div>

    <div :class="card" data-demo="live-controls">
      <div class="flex flex-wrap items-end gap-4">
        <label class="text-sm">
          <span class="mb-1 block text-xs font-medium text-slate-500">{{
            t("livestack.exposure")
          }}</span>
          <input
            v-model.number="exposureSec"
            type="number"
            min="1"
            step="1"
            :class="input"
          />
        </label>
        <button :class="btnPrimary" :disabled="!canStart" @click="start">
          {{ t("livestack.start") }}
        </button>
      </div>
      <p class="mt-2 text-xs text-slate-400">{{ t("livestack.startHint") }}</p>
    </div>
  </div>
</template>
