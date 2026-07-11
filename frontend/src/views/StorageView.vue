<script setup lang="ts">
// Storage hub — one page, four collapsible sections:
//   1. Connections        — connect to any S3-compatible store (encrypted server-side), test, set default.
//   2. Browse objects     — buckets & objects of the selected connection: upload/download/delete/new folder.
//   3. Local drives → S3  — browse a mounted drive and enqueue a content-verified copy to S3.
//   4. Backup & restore   — snapshot DB + calibration library + LP atlas + app settings to S3.
// The connection secret is write-only (sent on save/test, never returned).
import { onMounted, ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import {
  useS3ConnStore,
  type ConnForm,
  type S3Connection,
  type TestResult,
} from "@/stores/s3conn";
import { useDrivesStore } from "@/stores/drives";
import { useS3Store } from "@/stores/s3";
import { btnPrimary, btnGhost, input, checkbox } from "@/constants/styles";
import AccordionGroup from "@/components/Common/AccordionGroup.vue";
import BackupPanel from "@/components/Common/BackupPanel.vue";
import FileBrowser from "@/components/Common/FileBrowser.vue";
import Spinner from "@/components/Common/Spinner.vue";
import IconFolder from "@/components/Icons/IconFolder.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import IconArrowUp from "@/components/Icons/IconArrowUp.vue";
import IconCloud from "@/components/Icons/IconCloud.vue";
import IconX from "@/components/Icons/IconX.vue";
import type { BrowseEntry } from "@/types";

const { t } = useI18n();
const router = useRouter();
const store = useS3ConnStore();
const drives = useDrivesStore();
const s3 = useS3Store();

const accordion = ref<InstanceType<typeof AccordionGroup> | null>(null);
const sections = computed(() => [
  { key: "connections", title: t("storage.connections") },
  { key: "browse", title: t("storage.browseObjects") },
  { key: "drives", title: t("storage.drivesSection") },
  { key: "backup", title: t("backup.title") },
]);

// --- connection form ---
function blankForm(): ConnForm {
  return {
    name: "",
    endpoint: "",
    region: "us-east-1",
    access_key_id: "",
    secret_access_key: "",
    use_ssl: true,
    make_default: false,
  };
}
const form = ref<ConnForm | null>(null);
const editingId = ref<number | null>(null);
const testing = ref(false);
const testResult = ref<TestResult | null>(null);
const saving = ref(false);

function addConnection() {
  form.value = blankForm();
  editingId.value = null;
  testResult.value = null;
}
function editConnection(c: S3Connection) {
  form.value = {
    name: c.name,
    endpoint: c.endpoint,
    region: c.region,
    access_key_id: c.access_key_id,
    secret_access_key: "", // blank = keep the stored secret
    use_ssl: c.use_ssl,
    make_default: c.is_default,
  };
  editingId.value = c.id;
  testResult.value = null;
}
function cancelForm() {
  form.value = null;
  testResult.value = null;
}
async function testForm() {
  if (!form.value) return;
  testing.value = true;
  testResult.value = null;
  try {
    testResult.value = await store.test(form.value);
  } catch (e) {
    testResult.value = { ok: false, error: (e as Error).message };
  } finally {
    testing.value = false;
  }
}
async function saveForm() {
  if (!form.value || saving.value) return;
  saving.value = true;
  try {
    if (editingId.value != null)
      await store.update(editingId.value, form.value);
    else await store.create(form.value);
    form.value = null;
    testResult.value = null;
    void s3.fetchStatus(); // a new/changed default drives the Drives + Backup bucket picker
  } catch (e) {
    testResult.value = { ok: false, error: (e as Error).message };
  } finally {
    saving.value = false;
  }
}
async function removeConnection(c: S3Connection) {
  if (!window.confirm(t("storage.confirmDeleteConn", { name: c.name }))) return;
  if (selectedConn.value === c.id) closeBrowser();
  await store.remove(c.id);
}
async function makeDefault(c: S3Connection) {
  await store.setDefault(c.id);
  void s3.fetchStatus(); // keep the app-wide default (Drives/Backup bucket) in sync
}

// --- object browser ---
const selectedConn = ref<number | null>(null);
const selectedName = ref("");
const currentBucket = ref("");
const bucketList = ref<string[]>([]);
const browserError = ref("");
const browserLoading = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);

// FileBrowser (Miller columns, manage mode) model: currentPath is a clean full key ("" = bucket root);
// currentEntries are that folder's immediate children; selectedEntries are the checked rows (for a move).
// browseKey remounts the browser to drop its column cache after a mutation (delete/move/upload/new folder).
const currentPath = ref("");
const currentEntries = ref<BrowseEntry[]>([]);
const selectedEntries = ref<BrowseEntry[]>([]);
const browseKey = ref(0);

// fetchChildren binds the object lister to the active connection+bucket for the columns' lazy fan-out.
const fetchChildren = computed(() =>
  selectedConn.value != null && currentBucket.value
    ? store.browseChildren(selectedConn.value, currentBucket.value)
    : undefined,
);

// entryKey rebuilds the real S3 key from a BrowseEntry (folders carry no trailing slash in `path`).
function entryKey(e: BrowseEntry): string {
  return e.is_dir ? e.path + "/" : e.path;
}
function toggleSel(e: BrowseEntry) {
  const i = selectedEntries.value.findIndex((s) => s.path === e.path);
  if (i >= 0) selectedEntries.value.splice(i, 1);
  else selectedEntries.value.push(e);
}

async function openBrowser(c: S3Connection) {
  selectedConn.value = c.id;
  selectedName.value = c.name;
  currentBucket.value = "";
  currentPath.value = "";
  currentEntries.value = [];
  selectedEntries.value = [];
  browserError.value = "";
  accordion.value?.open("browse"); // reveal the browser section for the picked connection
  await loadBuckets();
}
function closeBrowser() {
  selectedConn.value = null;
}
async function loadBuckets() {
  if (selectedConn.value == null) return;
  browserLoading.value = true;
  browserError.value = "";
  try {
    bucketList.value = await store.buckets(selectedConn.value);
  } catch (e) {
    browserError.value = (e as Error).message;
  } finally {
    browserLoading.value = false;
  }
}
async function openBucket(b: string) {
  currentBucket.value = b;
  selectedEntries.value = [];
  await browseTo("");
}
async function browseTo(path: string) {
  currentPath.value = path;
  await loadCurrent();
}
async function loadCurrent() {
  const fc = fetchChildren.value;
  if (!fc) return;
  browserLoading.value = true;
  browserError.value = "";
  try {
    currentEntries.value = await fc(currentPath.value);
  } catch (e) {
    browserError.value = (e as Error).message;
    currentEntries.value = [];
  } finally {
    browserLoading.value = false;
  }
}
// refreshBrowser reloads the current folder and remounts the columns (drops the ancestor cache) so a
// moved/deleted/uploaded object is reflected everywhere, not just in the active column.
async function refreshBrowser() {
  browseKey.value++;
  await loadCurrent();
}

async function newBucket() {
  if (selectedConn.value == null) return;
  const name = window.prompt(t("storage.newBucketPrompt"));
  if (!name) return;
  try {
    await store.createBucket(selectedConn.value, name);
    await loadBuckets();
  } catch (e) {
    browserError.value = (e as Error).message;
  }
}
async function deleteBucket(b: string) {
  if (selectedConn.value == null) return;
  if (!window.confirm(t("storage.confirmDeleteBucket", { name: b }))) return;
  const force = window.confirm(t("storage.forceDeleteBucket"));
  try {
    await store.deleteBucket(selectedConn.value, b, force);
    if (currentBucket.value === b) currentBucket.value = "";
    await loadBuckets();
  } catch (e) {
    browserError.value = (e as Error).message;
  }
}
async function newFolder() {
  if (selectedConn.value == null || !currentBucket.value) return;
  const name = window.prompt(t("storage.newFolderPrompt"));
  if (!name) return;
  const parent = currentPath.value ? currentPath.value + "/" : "";
  try {
    await store.createFolder(
      selectedConn.value,
      currentBucket.value,
      parent + name.replace(/\/+$/, "") + "/",
    );
    await refreshBrowser();
  } catch (e) {
    browserError.value = (e as Error).message;
  }
}
async function onFilesPicked(e: Event) {
  const files = (e.target as HTMLInputElement).files;
  if (!files || selectedConn.value == null || !currentBucket.value) return;
  const parent = currentPath.value ? currentPath.value + "/" : "";
  browserLoading.value = true;
  try {
    for (const file of Array.from(files)) {
      await store.upload(
        selectedConn.value,
        currentBucket.value,
        parent + file.name,
        file,
      );
    }
    await refreshBrowser();
  } catch (err) {
    browserError.value = (err as Error).message;
  } finally {
    browserLoading.value = false;
    if (fileInput.value) fileInput.value.value = "";
  }
}
async function deleteEntry(e: BrowseEntry) {
  if (selectedConn.value == null) return;
  const msg = e.is_dir
    ? t("storage.confirmDeleteFolder", { name: e.name })
    : t("storage.confirmDeleteObject", { name: e.name });
  if (!window.confirm(msg)) return;
  try {
    await store.deleteObject(
      selectedConn.value,
      currentBucket.value,
      entryKey(e),
    );
    selectedEntries.value = selectedEntries.value.filter(
      (s) => s.path !== e.path,
    );
    await refreshBrowser();
  } catch (err) {
    browserError.value = (err as Error).message;
  }
}
function downloadHref(e: BrowseEntry): string {
  return selectedConn.value == null
    ? "#"
    : store.downloadUrl(selectedConn.value, currentBucket.value, entryKey(e));
}

// --- move (drag a row onto a folder, or the "Move to…" picker over the checked selection) ---
const moveOpen = ref(false);
const moveTargets = ref<BrowseEntry[]>([]); // the entries being moved
const movePickerPath = ref(""); // destination folder currently browsed in the dialog
const movePickerFolders = ref<BrowseEntry[]>([]); // subfolders at movePickerPath
const moving = ref(false);

function openMovePicker(entries: BrowseEntry[]) {
  if (!entries.length) return;
  moveTargets.value = entries;
  movePickerPath.value = "";
  moveOpen.value = true;
  void loadMovePicker();
}
async function loadMovePicker() {
  const fc = fetchChildren.value;
  if (!fc) return;
  movePickerFolders.value = (await fc(movePickerPath.value)).filter(
    (k) => k.is_dir,
  );
}
function movePickerInto(folder: BrowseEntry) {
  movePickerPath.value = folder.path;
  void loadMovePicker();
}
function movePickerUp() {
  const parts = movePickerPath.value.split("/").filter(Boolean);
  parts.pop();
  movePickerPath.value = parts.join("/");
  void loadMovePicker();
}
// runMove moves each src key into destFolder (a folder key, "" = bucket root), then refreshes.
async function runMove(srcKeys: string[], destFolder: string) {
  if (selectedConn.value == null || !currentBucket.value) return;
  moving.value = true;
  browserError.value = "";
  try {
    for (const src of srcKeys) {
      await store.move(
        selectedConn.value,
        currentBucket.value,
        src,
        destFolder,
      );
    }
    selectedEntries.value = [];
    await refreshBrowser();
  } catch (err) {
    browserError.value = (err as Error).message;
  } finally {
    moving.value = false;
  }
}
async function confirmMove() {
  const dest = movePickerPath.value ? movePickerPath.value + "/" : "";
  await runMove(moveTargets.value.map(entryKey), dest);
  moveOpen.value = false;
}
// onDragMove handles a FileBrowser drag-drop: src/dst are clean paths; srcIsDir re-adds the folder slash.
function onDragMove(p: { src: string; dst: string; srcIsDir: boolean }) {
  const srcKey = p.srcIsDir ? p.src + "/" : p.src;
  void runMove([srcKey], p.dst ? p.dst + "/" : "");
}

// --- local drives → S3 ---
const copyingPath = ref(""); // the folder currently being enqueued (disables its button)
const actionError = ref("");
const atDriveList = computed(() => drives.path === "");
const bucketReady = computed(() => s3.configured && s3.bucket !== "");

// fmtBytes renders a byte count in binary units, or "" when unknown (drive capacity / file size).
function fmtBytes(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

// copyFolder enqueues the verified copy of one folder and jumps to its job page (live progress bar).
async function copyFolder(sourcePath: string): Promise<void> {
  if (!bucketReady.value) return;
  actionError.value = "";
  copyingPath.value = sourcePath;
  try {
    const id = await drives.copyToS3(sourcePath, s3.bucket, s3.prefix);
    router.push({ name: "job", params: { id: String(id) } });
  } catch (e) {
    actionError.value = (e as Error).message;
  } finally {
    copyingPath.value = "";
  }
}

onMounted(() => {
  store.list();
  void s3.fetchStatus();
  void drives.loadDrives();
});
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold">{{ t("storage.title") }}</h1>
      <p class="text-sm text-slate-500">{{ t("storage.subtitle") }}</p>
    </div>

    <AccordionGroup
      ref="accordion"
      :items="sections"
      :default-open="['connections']"
      storage-key="astrostack.storage.sections"
    >
      <!-- 1. Connections -->
      <template #connections>
        <div class="mb-3 flex justify-end">
          <button :class="btnPrimary" @click="addConnection">
            {{ t("storage.addConnection") }}
          </button>
        </div>

        <p v-if="store.error" class="mb-2 text-xs text-danger-500">
          {{ store.error }}
        </p>
        <p
          v-if="!store.connections.length && !form"
          class="text-sm text-slate-400"
        >
          {{ t("storage.noConnections") }}
        </p>

        <ul
          v-if="store.connections.length"
          class="divide-y divide-slate-200 dark:divide-slate-700"
        >
          <li
            v-for="c in store.connections"
            :key="c.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2"
          >
            <div class="min-w-0">
              <p class="flex items-center gap-2 text-sm font-medium">
                {{ c.name }}
                <span
                  v-if="c.is_default"
                  class="rounded bg-brand-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-brand-700 dark:bg-brand-900 dark:text-brand-200"
                >
                  {{ t("storage.default") }}
                </span>
              </p>
              <p class="truncate text-xs text-slate-500">
                {{ c.endpoint || "AWS S3" }} · {{ c.region }} ·
                {{ c.access_key_id }}
              </p>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <button :class="btnPrimary" @click="openBrowser(c)">
                {{ t("storage.browse") }}
              </button>
              <button
                v-if="!c.is_default"
                :class="btnGhost"
                @click="makeDefault(c)"
              >
                {{ t("storage.setDefault") }}
              </button>
              <button :class="btnGhost" @click="editConnection(c)">
                {{ t("storage.edit") }}
              </button>
              <button :class="btnGhost" @click="removeConnection(c)">
                {{ t("storage.delete") }}
              </button>
            </div>
          </li>
        </ul>

        <!-- Add / edit form -->
        <div
          v-if="form"
          class="mt-4 rounded-lg border border-slate-200 p-4 dark:border-slate-700"
        >
          <h3 class="mb-3 text-sm font-semibold">
            {{
              editingId
                ? t("storage.editConnection")
                : t("storage.newConnection")
            }}
          </h3>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="text-sm">
              <span class="mb-1 block text-xs font-medium text-slate-500">{{
                t("storage.name")
              }}</span>
              <input v-model="form.name" :class="input" />
            </label>
            <label class="text-sm">
              <span class="mb-1 block text-xs font-medium text-slate-500">{{
                t("storage.endpoint")
              }}</span>
              <input
                v-model="form.endpoint"
                :class="input"
                placeholder="s3.amazonaws.com / localhost:9000"
              />
            </label>
            <label class="text-sm">
              <span class="mb-1 block text-xs font-medium text-slate-500">{{
                t("storage.region")
              }}</span>
              <input v-model="form.region" :class="input" />
            </label>
            <label class="text-sm">
              <span class="mb-1 block text-xs font-medium text-slate-500">{{
                t("storage.accessKey")
              }}</span>
              <input
                v-model="form.access_key_id"
                :class="input"
                autocomplete="off"
              />
            </label>
            <label class="text-sm">
              <span class="mb-1 block text-xs font-medium text-slate-500">{{
                t("storage.secretKey")
              }}</span>
              <input
                v-model="form.secret_access_key"
                type="password"
                :class="input"
                autocomplete="new-password"
                :placeholder="editingId ? t('storage.secretKeep') : ''"
              />
            </label>
            <div class="flex items-end gap-4 text-sm">
              <label class="flex items-center gap-2">
                <input
                  v-model="form.use_ssl"
                  type="checkbox"
                  :class="checkbox"
                />
                {{ t("storage.useSSL") }}
              </label>
              <label class="flex items-center gap-2">
                <input
                  v-model="form.make_default"
                  type="checkbox"
                  :class="checkbox"
                />
                {{ t("storage.makeDefault") }}
              </label>
            </div>
          </div>
          <div class="mt-3 flex flex-wrap items-center gap-3">
            <button :class="btnGhost" :disabled="testing" @click="testForm">
              {{ testing ? t("storage.testing") : t("storage.test") }}
            </button>
            <button :class="btnPrimary" :disabled="saving" @click="saveForm">
              {{ t("storage.save") }}
            </button>
            <button :class="btnGhost" @click="cancelForm">
              {{ t("storage.cancel") }}
            </button>
            <span
              v-if="testResult"
              class="text-xs"
              :class="
                testResult.ok
                  ? 'text-success-600 dark:text-success-300'
                  : 'text-danger-500'
              "
            >
              {{
                testResult.ok
                  ? t("storage.testOk", { n: testResult.buckets?.length ?? 0 })
                  : t("storage.testFail", { error: testResult.error })
              }}
            </span>
          </div>
        </div>
      </template>

      <!-- 2. Browse objects -->
      <template #browse>
        <p v-if="selectedConn == null" class="text-sm text-slate-400">
          {{ t("storage.browseHint") }}
        </p>
        <template v-else>
          <div class="mb-3 flex items-center justify-between">
            <h3 class="text-sm font-semibold">
              {{ t("storage.browsing", { name: selectedName }) }}
            </h3>
            <button
              class="text-slate-400 hover:text-slate-600"
              :title="t('common.close')"
              @click="closeBrowser"
            >
              <IconX class="h-5 w-5" />
            </button>
          </div>

          <p v-if="browserError" class="mb-2 text-xs text-danger-500">
            {{ browserError }}
          </p>

          <!-- Bucket picker -->
          <div v-if="!currentBucket" class="space-y-2">
            <div class="flex items-center gap-2">
              <h4 class="text-xs font-medium uppercase text-slate-500">
                {{ t("storage.buckets") }}
              </h4>
              <button :class="btnGhost" @click="newBucket">
                {{ t("storage.newBucket") }}
              </button>
              <button :class="btnGhost" @click="loadBuckets">
                {{ t("storage.refresh") }}
              </button>
            </div>
            <p v-if="!bucketList.length" class="text-sm text-slate-400">
              {{ t("storage.noBuckets") }}
            </p>
            <ul v-else class="divide-y divide-slate-200 dark:divide-slate-700">
              <li
                v-for="b in bucketList"
                :key="b"
                class="flex items-center justify-between py-2"
              >
                <button
                  class="flex items-center gap-2 text-sm font-medium hover:text-brand-600"
                  @click="openBucket(b)"
                >
                  <IconFolder class="h-4 w-4 text-slate-400" />
                  {{ b }}
                </button>
                <button :class="btnGhost" @click="deleteBucket(b)">
                  {{ t("storage.delete") }}
                </button>
              </li>
            </ul>
          </div>

          <!-- Object list (Miller columns via the shared FileBrowser, manage mode) -->
          <div v-else class="space-y-2">
            <!-- Toolbar -->
            <div class="flex flex-wrap items-center gap-2">
              <button
                :class="btnGhost"
                @click="
                  currentBucket = '';
                  currentEntries = [];
                  selectedEntries = [];
                "
              >
                {{ t("storage.allBuckets") }}
              </button>
              <span class="text-sm font-medium">{{ currentBucket }}</span>
              <span class="flex-1"></span>
              <span
                v-if="selectedEntries.length"
                class="text-xs text-slate-500"
              >
                {{ t("import.selectedCount", { n: selectedEntries.length }) }}
              </span>
              <button
                v-if="selectedEntries.length"
                :class="btnGhost"
                @click="openMovePicker(selectedEntries)"
              >
                {{ t("storage.move.moveSelected") }}
              </button>
              <button :class="btnGhost" @click="newFolder">
                {{ t("storage.newFolder") }}
              </button>
              <button :class="btnGhost" @click="fileInput?.click()">
                {{ t("storage.upload") }}
              </button>
              <button :class="btnGhost" @click="refreshBrowser">
                {{ t("storage.refresh") }}
              </button>
              <input
                ref="fileInput"
                type="file"
                multiple
                class="hidden"
                @change="onFilesPicked"
              />
            </div>

            <p class="text-xs text-slate-400">{{ t("storage.move.hint") }}</p>

            <FileBrowser
              :key="browseKey"
              manage
              :path="currentPath"
              root=""
              :entries="currentEntries"
              :loading="browserLoading"
              :selected="selectedEntries"
              :fetch-children="fetchChildren"
              @navigate="browseTo"
              @toggle="toggleSel"
              @clear-selection="selectedEntries = []"
              @move="onDragMove"
            >
              <template #rowActions="{ entry }">
                <a
                  v-if="!entry.is_dir"
                  :href="downloadHref(entry)"
                  :title="t('storage.download')"
                  class="rounded p-1 text-slate-400 hover:text-brand-600"
                >
                  <IconDownload class="h-3.5 w-3.5" />
                </a>
                <button
                  :title="t('storage.delete')"
                  class="rounded p-1 text-slate-400 hover:text-danger-500"
                  @click="deleteEntry(entry)"
                >
                  <IconX class="h-3.5 w-3.5" />
                </button>
              </template>
            </FileBrowser>

            <!-- Move-to picker: navigate to an EXISTING destination folder, then "Move here". -->
            <div
              v-if="moveOpen"
              class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
              @click.self="moveOpen = false"
            >
              <div
                class="w-full max-w-md space-y-3 rounded-xl border border-slate-200 bg-white p-4 shadow-xl dark:border-slate-700 dark:bg-slate-900"
              >
                <h3 class="text-sm font-semibold">
                  {{ t("storage.move.title", { n: moveTargets.length }) }}
                </h3>
                <div class="flex flex-wrap items-center gap-1 text-xs">
                  <button
                    class="font-medium hover:text-brand-600"
                    @click="
                      movePickerPath = '';
                      loadMovePicker();
                    "
                  >
                    {{ currentBucket }}
                  </button>
                  <template
                    v-for="(seg, i) in movePickerPath
                      .split('/')
                      .filter(Boolean)"
                    :key="i"
                  >
                    <span class="text-slate-300">/</span>
                    <span class="text-slate-600 dark:text-slate-300">{{
                      seg
                    }}</span>
                  </template>
                  <span class="flex-1"></span>
                  <button
                    v-if="movePickerPath"
                    :class="btnGhost"
                    class="!px-2 !py-0.5"
                    :title="t('storage.up')"
                    @click="movePickerUp"
                  >
                    <IconArrowUp class="h-3.5 w-3.5" />
                  </button>
                </div>
                <ul
                  class="max-h-60 divide-y divide-slate-100 overflow-y-auto rounded border border-slate-200 dark:divide-slate-800 dark:border-slate-700"
                >
                  <li v-for="f in movePickerFolders" :key="f.path">
                    <button
                      class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-slate-50 dark:hover:bg-slate-800"
                      @click="movePickerInto(f)"
                    >
                      <IconFolder class="h-4 w-4 shrink-0 text-brand-500" />
                      <span class="truncate">{{ f.name }}</span>
                    </button>
                  </li>
                  <li
                    v-if="!movePickerFolders.length"
                    class="px-3 py-4 text-center text-xs text-slate-400"
                  >
                    {{ t("storage.move.noSubfolders") }}
                  </li>
                </ul>
                <div class="flex items-center justify-end gap-2">
                  <button :class="btnGhost" @click="moveOpen = false">
                    {{ t("common.cancel") }}
                  </button>
                  <button
                    :class="btnPrimary"
                    :disabled="moving"
                    @click="confirmMove"
                  >
                    {{ t("storage.move.moveHere") }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </template>
      </template>

      <!-- 3. Local drives → S3 -->
      <template #drives>
        <!-- S3 target: the same bucket/prefix the app uses for backups & transfers. -->
        <div class="mb-4">
          <h3 class="mb-2 text-xs font-medium uppercase text-slate-500">
            {{ t("drives.target") }}
          </h3>
          <p
            v-if="!s3.configured"
            class="text-sm text-slate-500 dark:text-slate-400"
          >
            {{ t("drives.noS3") }} {{ t("drives.connectHere") }}
          </p>
          <div v-else class="flex flex-wrap items-end gap-4">
            <label
              class="flex flex-col gap-1 text-xs text-slate-500 dark:text-slate-400"
            >
              {{ t("drives.bucket") }}
              <select
                :class="input"
                :value="s3.bucket"
                @change="
                  s3.setBucket(($event.target as HTMLSelectElement).value)
                "
              >
                <option value="" disabled>{{ t("drives.pickBucket") }}</option>
                <option v-for="b in s3.buckets" :key="b" :value="b">
                  {{ b }}
                </option>
              </select>
            </label>
            <label
              class="flex flex-col gap-1 text-xs text-slate-500 dark:text-slate-400"
            >
              {{ t("drives.prefix") }}
              <input
                :class="input"
                :value="s3.prefix"
                placeholder="astro"
                @change="
                  s3.setPrefix(($event.target as HTMLInputElement).value)
                "
              />
            </label>
            <p class="max-w-xs text-xs text-slate-400 dark:text-slate-500">
              {{ t("drives.smartHint") }}
            </p>
          </div>
          <p
            v-if="s3.configured && !s3.buckets.length"
            class="mt-2 text-xs text-amber-600 dark:text-amber-400"
          >
            {{ t("drives.noBuckets") }}
          </p>
        </div>

        <!-- Browser: drive grid at the root, folder contents below one. -->
        <div>
          <div class="mb-3 flex items-center gap-2">
            <button
              v-if="!atDriveList"
              :class="btnGhost"
              :title="t('drives.up')"
              @click="drives.up()"
            >
              <IconArrowUp class="h-4 w-4" />
            </button>
            <span
              class="min-w-0 flex-1 truncate text-sm text-slate-600 dark:text-slate-300"
              :title="atDriveList ? '' : drives.path"
            >
              {{ atDriveList ? t("drives.drivesHeading") : drives.path }}
            </span>
            <button
              :class="btnGhost"
              @click="
                atDriveList ? drives.loadDrives() : drives.browse(drives.path)
              "
            >
              {{ t("drives.refresh") }}
            </button>
            <button
              v-if="!atDriveList"
              :class="btnPrimary"
              :disabled="!bucketReady || copyingPath === drives.path"
              :title="
                bucketReady
                  ? t('drives.copyFolderHint')
                  : t('drives.pickBucketFirst')
              "
              @click="copyFolder(drives.path)"
            >
              <IconCloud class="h-4 w-4" />
              {{ t("drives.copyThisFolder") }}
            </button>
          </div>

          <div
            v-if="drives.error || actionError"
            class="mb-3 rounded-md bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-900/30 dark:text-red-300"
          >
            {{ drives.error || actionError }}
          </div>

          <div v-if="drives.loading" class="py-6 text-center">
            <Spinner>{{ t("common.loading") }}</Spinner>
          </div>

          <!-- Drive list -->
          <div v-else-if="atDriveList">
            <p
              v-if="!drives.drives.length"
              class="py-6 text-center text-sm text-slate-400"
            >
              {{ t("drives.noDrives") }}
            </p>
            <ul v-else class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <li v-for="d in drives.drives" :key="d.path">
                <button
                  class="flex w-full items-center gap-3 rounded-lg border border-slate-200 p-3 text-left transition-colors hover:border-brand-400 dark:border-slate-700 dark:hover:border-brand-500"
                  @click="drives.browse(d.path)"
                >
                  <IconFolder class="h-6 w-6 shrink-0 text-brand-500" />
                  <span class="min-w-0 flex-1">
                    <span
                      class="block truncate text-sm font-medium text-slate-800 dark:text-slate-100"
                      >{{ d.name }}</span
                    >
                    <span
                      v-if="fmtBytes(d.total_bytes)"
                      class="block text-xs text-slate-400"
                    >
                      {{
                        t("drives.capacity", {
                          free: fmtBytes(d.free_bytes),
                          total: fmtBytes(d.total_bytes),
                        })
                      }}
                    </span>
                  </span>
                </button>
              </li>
            </ul>
          </div>

          <!-- Folder contents -->
          <div v-else>
            <p
              v-if="!drives.entries.length"
              class="py-6 text-center text-sm text-slate-400"
            >
              {{ t("drives.emptyFolder") }}
            </p>
            <ul
              v-else
              class="max-h-[32rem] divide-y divide-slate-200 overflow-y-auto dark:divide-slate-700"
            >
              <li
                v-for="e in drives.entries"
                :key="e.path"
                class="flex items-center justify-between gap-2 py-1.5"
              >
                <button
                  v-if="e.is_dir"
                  class="flex min-w-0 items-center gap-2 text-sm font-medium hover:text-brand-600"
                  @click="drives.browse(e.path)"
                >
                  <IconFolder class="h-4 w-4 shrink-0 text-slate-400" />
                  <span class="truncate">{{ e.name }}</span>
                </button>
                <span
                  v-else
                  class="flex min-w-0 items-center gap-2 text-sm text-slate-600 dark:text-slate-300"
                >
                  <IconFile class="h-4 w-4 shrink-0 text-slate-400" />
                  <span class="truncate">{{ e.name }}</span>
                  <span
                    v-if="fmtBytes(e.size)"
                    class="shrink-0 text-xs text-slate-400"
                    >{{ fmtBytes(e.size) }}</span
                  >
                </span>
                <button
                  v-if="e.is_dir"
                  :class="btnGhost"
                  :disabled="!bucketReady || copyingPath === e.path"
                  :title="
                    bucketReady
                      ? t('drives.copyFolderHint')
                      : t('drives.pickBucketFirst')
                  "
                  @click="copyFolder(e.path)"
                >
                  <IconCloud class="h-4 w-4" />
                  <span>{{
                    copyingPath === e.path
                      ? t("drives.copying")
                      : t("drives.copy")
                  }}</span>
                </button>
              </li>
            </ul>
          </div>
        </div>
      </template>

      <!-- 4. Backup & restore -->
      <template #backup>
        <BackupPanel v-if="s3.active" embedded />
        <p v-else class="text-sm text-slate-400">
          {{ t("storage.backupNeedsS3") }}
        </p>
      </template>
    </AccordionGroup>
  </div>
</template>
