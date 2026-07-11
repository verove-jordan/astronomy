<script setup lang="ts">
// Backup-everything panel: snapshot the Postgres database, calibration library, light-pollution atlas and
// the browser-only app state (favorites/setups/prefs + AI chats) to S3, and restore any past snapshot.
// Server pieces run as jobs (progress in Tasks); the browser app state is gathered/applied client-side.
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  useBackupStore,
  type BackupComponent,
  type BackupManifest,
} from "@/stores/backup";
import { card, btnPrimary, btnGhost, checkbox } from "@/constants/styles";
import IconCloud from "@/components/Icons/IconCloud.vue";

// embedded drops the card frame + header so the panel can sit inside an existing card/accordion section
// (the Storage page) that already provides them; standalone it renders its own card + title.
defineProps<{ embedded?: boolean }>();

const { t } = useI18n();
const store = useBackupStore();

const ALL: BackupComponent[] = ["db", "library", "atlas", "appstate"];
const selected = ref<Record<BackupComponent, boolean>>({
  db: true,
  library: true,
  atlas: true,
  appstate: true,
});
const busy = ref(false);
const toast = ref("");
const restoringStamp = ref("");
const needsReload = ref(false);

onMounted(() => store.list());

function chosen(): BackupComponent[] {
  return ALL.filter((c) => selected.value[c]);
}

async function runBackup() {
  const comps = chosen();
  if (!comps.length || busy.value) return;
  busy.value = true;
  toast.value = "";
  try {
    await store.backup(comps);
    toast.value = t("backup.queued");
    // The manifest lands only when the job finishes — refresh the list shortly after.
    setTimeout(() => void store.list(), 2000);
  } catch (e) {
    toast.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

async function runRestore(m: BackupManifest) {
  if (restoringStamp.value || !window.confirm(t("backup.confirmRestore")))
    return;
  restoringStamp.value = m.stamp;
  toast.value = "";
  try {
    await store.restore(m.stamp, m.components as BackupComponent[]);
    toast.value = t("backup.restoreQueued");
    if (m.components.includes("appstate")) needsReload.value = true;
  } catch (e) {
    toast.value = (e as Error).message;
  } finally {
    restoringStamp.value = "";
  }
}

function fmtDate(ms: number): string {
  return new Date(ms).toLocaleString();
}

function reloadPage() {
  window.location.reload();
}
</script>

<template>
  <section :class="embedded ? '' : card">
    <header v-if="!embedded" class="mb-3 flex items-center gap-2">
      <IconCloud class="h-5 w-5 text-brand-500" />
      <div>
        <h2 class="text-sm font-semibold">{{ t("backup.title") }}</h2>
        <p class="text-xs text-slate-500">{{ t("backup.subtitle") }}</p>
      </div>
    </header>
    <p v-if="embedded" class="mb-3 text-xs text-slate-500">
      {{ t("backup.subtitle") }}
    </p>

    <!-- Component checkboxes -->
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-sm">
      <label v-for="c in ALL" :key="c" class="flex items-center gap-2">
        <input v-model="selected[c]" type="checkbox" :class="checkbox" />
        {{ t("backup.component." + c) }}
      </label>
    </div>
    <p class="mt-2 text-xs text-slate-400">{{ t("backup.secretsNote") }}</p>

    <div class="mt-3 flex items-center gap-3">
      <button
        :class="btnPrimary"
        :disabled="busy || !chosen().length"
        @click="runBackup"
      >
        {{ busy ? t("backup.working") : t("backup.backupNow") }}
      </button>
      <button :class="btnGhost" :disabled="store.loading" @click="store.list()">
        {{ t("backup.refresh") }}
      </button>
      <span v-if="toast" class="text-xs text-success-600 dark:text-success-300">
        {{ toast }}
        <router-link
          :to="{ name: 'jobs' }"
          class="font-medium underline hover:text-success-700"
        >
          {{ t("import.viewQueue") }}
        </router-link>
      </span>
    </div>

    <p
      v-if="needsReload"
      class="mt-2 text-xs text-amber-600 dark:text-amber-400"
    >
      {{ t("backup.reloadHint") }}
      <button class="font-medium underline" @click="reloadPage">
        {{ t("backup.reload") }}
      </button>
    </p>

    <!-- Past backups -->
    <div class="mt-4">
      <h3 class="mb-1 text-xs font-medium text-slate-500">
        {{ t("backup.past") }}
      </h3>
      <p v-if="!store.backups.length" class="text-xs text-slate-400">
        {{ t("backup.empty") }}
      </p>
      <ul v-else class="divide-y divide-slate-200 dark:divide-slate-700">
        <li
          v-for="m in store.backups"
          :key="m.stamp"
          class="flex items-center justify-between gap-3 py-2"
        >
          <div class="min-w-0">
            <p class="truncate text-sm font-medium">
              {{ fmtDate(m.stamp_ms) }}
            </p>
            <p class="truncate text-xs text-slate-500">
              {{
                m.components.map((c) => t("backup.component." + c)).join(" · ")
              }}
            </p>
          </div>
          <button
            :class="btnGhost"
            :disabled="!!restoringStamp"
            @click="runRestore(m)"
          >
            {{
              restoringStamp === m.stamp
                ? t("backup.working")
                : t("backup.restore")
            }}
          </button>
        </li>
      </ul>
    </div>
  </section>
</template>
