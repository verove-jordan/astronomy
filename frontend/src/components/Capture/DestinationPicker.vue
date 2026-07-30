<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import IconFolder from "@/components/Icons/IconFolder.vue";
import { btnGhost, btnPrimary, input } from "@/constants/styles";
import { useDrivesStore } from "@/stores/drives";

// Where the night's frames go. It has to be a real browser, not a text field: captures routinely
// land on an external disk, and typing an absolute path in the dark is how a session ends up in the
// wrong place. It browses the same locations the rest of the app can reach — the app's own input
// folder and any connected drive — and the backend re-validates whatever is chosen.
//
// The destination usually does not exist yet (tonight's date, a new tile), so a "new folder" field
// appends a subfolder to the browsed path; the engine creates it when the run starts.
const { t } = useI18n();
const drives = useDrivesStore();

const model = defineModel<string>({ default: "" });
const open = ref(false);
const newFolder = ref("");

onMounted(() => {
  void drives.loadSources();
  void drives.loadDrives();
});

// The path that would be used: the browsed folder plus any typed subfolder.
const candidate = computed(() => {
  const base = drives.path;
  const sub = newFolder.value.trim().replace(/^\/+|\/+$/g, "");
  if (!base) return "";
  return sub ? `${base}/${sub}` : base;
});

const folders = computed(() => drives.entries.filter((e) => e.is_dir));

function sourceLabel(key: string): string {
  return t(`capture.dest.source_${key}`);
}

function choose() {
  if (!candidate.value) return;
  model.value = candidate.value;
  open.value = false;
  newFolder.value = "";
}
</script>

<template>
  <div>
    <label class="text-xs text-slate-500 dark:text-slate-400">
      {{ t("capture.run.path") }}
      <div class="mt-0.5 flex gap-2">
        <input
          v-model="model"
          :class="input"
          class="flex-1 font-mono text-xs"
          :placeholder="t('capture.dest.placeholder')"
        />
        <button
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="open = !open"
        >
          {{ t("capture.dest.browse") }}
        </button>
      </div>
    </label>

    <div
      v-if="open"
      class="mt-2 rounded-md border border-slate-200 p-2 dark:border-slate-700"
    >
      <!-- Roots: the app's own folders and every connected drive -->
      <div class="flex flex-wrap gap-1">
        <button
          v-for="s in drives.sources"
          :key="s.path"
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="drives.enterRoot(s.path)"
        >
          {{ sourceLabel(s.key) }}
        </button>
        <button
          v-for="d in drives.drives"
          :key="d.path"
          :class="btnGhost"
          class="!px-2 !py-1 text-xs"
          @click="drives.enterRoot(d.path)"
        >
          {{ d.name }}
        </button>
        <span
          v-if="!drives.sources.length && !drives.drives.length"
          class="text-xs text-slate-400"
          >{{ t("capture.dest.noRoots") }}</span
        >
      </div>

      <template v-if="drives.path">
        <div class="mt-2 flex items-center gap-2 text-xs">
          <button
            :class="btnGhost"
            class="!px-2 !py-0.5"
            :disabled="!drives.parent"
            @click="drives.up()"
          >
            ↑
          </button>
          <span
            class="min-w-0 flex-1 truncate font-mono text-slate-500 dark:text-slate-400"
            >{{ drives.path }}</span
          >
        </div>

        <ul class="mt-1 max-h-48 overflow-y-auto">
          <li v-for="f in folders" :key="f.path">
            <button
              class="flex w-full items-center gap-2 rounded px-1.5 py-1 text-left text-xs hover:bg-slate-100 dark:hover:bg-slate-700"
              @click="drives.browse(f.path)"
            >
              <IconFolder class="h-4 w-4 shrink-0 text-slate-400" />
              <span class="truncate">{{ f.name }}</span>
            </button>
          </li>
          <li v-if="!folders.length" class="px-1.5 py-1 text-xs text-slate-400">
            {{ t("capture.dest.empty") }}
          </li>
        </ul>

        <div class="mt-2 flex flex-wrap items-center gap-2">
          <label class="flex-1 text-xs text-slate-500 dark:text-slate-400">
            {{ t("capture.dest.newFolder") }}
            <input
              v-model="newFolder"
              :class="input"
              :placeholder="t('capture.dest.newFolderPlaceholder')"
            />
          </label>
          <button
            :class="btnPrimary"
            class="!px-3 !py-1 text-xs"
            @click="choose"
          >
            {{ t("capture.dest.use") }}
          </button>
        </div>
        <p class="mt-1 truncate font-mono text-[11px] text-slate-400">
          {{ candidate }}
        </p>
      </template>

      <p v-if="drives.error" class="mt-1 text-xs text-danger-500">
        {{ drives.error }}
      </p>
      <p class="mt-1 text-[11px] text-slate-400">
        {{ t("capture.dest.hint") }}
      </p>
    </div>
  </div>
</template>
