<script setup lang="ts">
// S3 storage manager: connect to any S3-compatible store by entering endpoint + access key + secret
// (stored encrypted server-side), test the connection, mark one default (which also drives the pipeline),
// and browse/manage its buckets & objects — upload, download, delete, create folder/bucket. The secret key
// is write-only: it's sent on save/test and never returned.
import { onMounted, ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import {
  useS3ConnStore,
  type ConnForm,
  type S3Connection,
  type S3ObjectEntry,
  type TestResult,
} from "@/stores/s3conn";
import {
  card,
  btnPrimary,
  btnGhost,
  input,
  checkbox,
} from "@/constants/styles";
import IconFolder from "@/components/Icons/IconFolder.vue";
import IconFile from "@/components/Icons/IconFile.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import IconArrowUp from "@/components/Icons/IconArrowUp.vue";
import IconX from "@/components/Icons/IconX.vue";

const { t } = useI18n();
const store = useS3ConnStore();

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
}

// --- object browser ---
const selectedConn = ref<number | null>(null);
const selectedName = ref("");
const currentBucket = ref("");
const currentPrefix = ref(""); // folder path within the bucket ("" = bucket root)
const bucketList = ref<string[]>([]);
const objectList = ref<S3ObjectEntry[]>([]);
const browserError = ref("");
const browserLoading = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);

const crumbs = computed(() => {
  const parts = currentPrefix.value.split("/").filter(Boolean);
  const acc: { name: string; prefix: string }[] = [];
  let p = "";
  for (const part of parts) {
    p += part + "/";
    acc.push({ name: part, prefix: p });
  }
  return acc;
});

async function openBrowser(c: S3Connection) {
  selectedConn.value = c.id;
  selectedName.value = c.name;
  currentBucket.value = "";
  currentPrefix.value = "";
  objectList.value = [];
  browserError.value = "";
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
  currentPrefix.value = "";
  await loadObjects();
}
async function loadObjects() {
  if (selectedConn.value == null || !currentBucket.value) return;
  browserLoading.value = true;
  browserError.value = "";
  try {
    objectList.value = await store.objects(
      selectedConn.value,
      currentBucket.value,
      currentPrefix.value,
    );
  } catch (e) {
    browserError.value = (e as Error).message;
  } finally {
    browserLoading.value = false;
  }
}
function enterFolder(o: S3ObjectEntry) {
  currentPrefix.value = o.key.endsWith("/") ? o.key : o.key + "/";
  loadObjects();
}
function goCrumb(prefix: string) {
  currentPrefix.value = prefix;
  loadObjects();
}
function upFolder() {
  const parts = currentPrefix.value.split("/").filter(Boolean);
  parts.pop();
  currentPrefix.value = parts.length ? parts.join("/") + "/" : "";
  loadObjects();
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
  try {
    await store.createFolder(
      selectedConn.value,
      currentBucket.value,
      currentPrefix.value + name.replace(/\/+$/, "") + "/",
    );
    await loadObjects();
  } catch (e) {
    browserError.value = (e as Error).message;
  }
}
async function onFilesPicked(e: Event) {
  const files = (e.target as HTMLInputElement).files;
  if (!files || selectedConn.value == null || !currentBucket.value) return;
  browserLoading.value = true;
  try {
    for (const file of Array.from(files)) {
      await store.upload(
        selectedConn.value,
        currentBucket.value,
        currentPrefix.value + file.name,
        file,
      );
    }
    await loadObjects();
  } catch (err) {
    browserError.value = (err as Error).message;
  } finally {
    browserLoading.value = false;
    if (fileInput.value) fileInput.value.value = "";
  }
}
async function deleteObject(o: S3ObjectEntry) {
  if (selectedConn.value == null) return;
  const msg = o.is_dir
    ? t("storage.confirmDeleteFolder", { name: o.name })
    : t("storage.confirmDeleteObject", { name: o.name });
  if (!window.confirm(msg)) return;
  try {
    await store.deleteObject(selectedConn.value, currentBucket.value, o.key);
    await loadObjects();
  } catch (e) {
    browserError.value = (e as Error).message;
  }
}
function downloadHref(o: S3ObjectEntry): string {
  return selectedConn.value == null
    ? "#"
    : store.downloadUrl(selectedConn.value, currentBucket.value, o.key);
}
function fmtSize(n?: number): string {
  if (!n) return "";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`;
}

onMounted(() => store.list());
</script>

<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold">{{ t("storage.title") }}</h1>
      <p class="text-sm text-slate-500">{{ t("storage.subtitle") }}</p>
    </div>

    <!-- Connections -->
    <section :class="card">
      <div class="mb-3 flex items-center justify-between">
        <h2 class="text-sm font-semibold">{{ t("storage.connections") }}</h2>
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
            editingId ? t("storage.editConnection") : t("storage.newConnection")
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
              <input v-model="form.use_ssl" type="checkbox" :class="checkbox" />
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
    </section>

    <!-- Object browser -->
    <section v-if="selectedConn != null" :class="card">
      <div class="mb-3 flex items-center justify-between">
        <h2 class="text-sm font-semibold">
          {{ t("storage.browsing", { name: selectedName }) }}
        </h2>
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
          <h3 class="text-xs font-medium uppercase text-slate-500">
            {{ t("storage.buckets") }}
          </h3>
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

      <!-- Object list -->
      <div v-else class="space-y-2">
        <!-- Toolbar + breadcrumb -->
        <div class="flex flex-wrap items-center gap-2">
          <button
            :class="btnGhost"
            @click="
              currentBucket = '';
              objectList = [];
            "
          >
            {{ t("storage.allBuckets") }}
          </button>
          <span class="text-slate-300">/</span>
          <button
            class="text-sm font-medium hover:text-brand-600"
            @click="goCrumb('')"
          >
            {{ currentBucket }}
          </button>
          <template v-for="cr in crumbs" :key="cr.prefix">
            <span class="text-slate-300">/</span>
            <button
              class="text-sm hover:text-brand-600"
              @click="goCrumb(cr.prefix)"
            >
              {{ cr.name }}
            </button>
          </template>
          <span class="flex-1"></span>
          <button
            v-if="currentPrefix"
            :class="btnGhost"
            :title="t('storage.up')"
            @click="upFolder"
          >
            <IconArrowUp class="h-4 w-4" />
          </button>
          <button :class="btnGhost" @click="newFolder">
            {{ t("storage.newFolder") }}
          </button>
          <button :class="btnGhost" @click="fileInput?.click()">
            {{ t("storage.upload") }}
          </button>
          <button :class="btnGhost" @click="loadObjects">
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

        <p
          v-if="!objectList.length && !browserLoading"
          class="py-4 text-center text-sm text-slate-400"
        >
          {{ t("storage.emptyFolder") }}
        </p>
        <ul
          v-else
          class="max-h-[28rem] divide-y divide-slate-200 overflow-y-auto dark:divide-slate-700"
        >
          <li
            v-for="o in objectList"
            :key="o.key"
            class="flex items-center justify-between gap-2 py-1.5"
          >
            <button
              v-if="o.is_dir"
              class="flex min-w-0 items-center gap-2 text-sm font-medium hover:text-brand-600"
              @click="enterFolder(o)"
            >
              <IconFolder class="h-4 w-4 shrink-0 text-slate-400" />
              <span class="truncate">{{ o.name }}</span>
            </button>
            <span v-else class="flex min-w-0 items-center gap-2 text-sm">
              <IconFile class="h-4 w-4 shrink-0 text-slate-400" />
              <span class="truncate">{{ o.name }}</span>
              <span class="shrink-0 text-xs text-slate-400">{{
                fmtSize(o.size)
              }}</span>
            </span>
            <div class="flex shrink-0 items-center gap-1">
              <a
                v-if="!o.is_dir"
                :href="downloadHref(o)"
                :title="t('storage.download')"
                class="rounded p-1 text-slate-400 hover:text-brand-600"
              >
                <IconDownload class="h-4 w-4" />
              </a>
              <button
                :title="t('storage.delete')"
                class="rounded p-1 text-slate-400 hover:text-danger-500"
                @click="deleteObject(o)"
              >
                <IconX class="h-4 w-4" />
              </button>
            </div>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>
