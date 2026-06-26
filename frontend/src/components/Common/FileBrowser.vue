<script setup lang="ts">
import { useI18n } from "vue-i18n";
import Breadcrumb from "@/components/Common/Breadcrumb.vue";
import Spinner from "@/components/Common/Spinner.vue";
import IconFolder from "@/components/Icons/IconFolder.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import IconArrowUp from "@/components/Icons/IconArrowUp.vue";
import { btnGhost, btnPrimary, input } from "@/constants/styles";
import type { BrowseEntry } from "@/types";

const props = defineProps<{
  path: string;
  root: string;
  entries: BrowseEntry[];
  loading: boolean;
  selected: string;
  error?: string;
}>();

const emit = defineEmits<{
  (e: "navigate", path: string): void;
  (e: "inspect", path: string): void;
}>();

const { t } = useI18n();

// editable address bar (paste a path), kept in sync via the path prop
function goPath(e: Event) {
  emit("navigate", (e.target as HTMLInputElement).value.trim());
}
function up() {
  const parent = props.path.replace(/\/[^/]+$/, "");
  if (parent) emit("navigate", parent);
}
</script>

<template>
  <div class="space-y-3">
    <!-- address bar -->
    <div class="flex flex-wrap items-end gap-2">
      <div class="min-w-0 grow">
        <label class="mb-1 block text-xs font-medium text-slate-500">{{
          t("common.path")
        }}</label>
        <input
          :value="path"
          :class="input"
          type="text"
          spellcheck="false"
          @keyup.enter="goPath"
        />
      </div>
      <button :class="btnGhost" :disabled="loading" @click="up">
        <IconArrowUp /> {{ t("common.up") }}
      </button>
      <button
        :class="btnPrimary"
        :disabled="loading || !path"
        @click="emit('inspect', path)"
      >
        {{ t("import.useFolder") }}
      </button>
    </div>

    <Breadcrumb
      v-if="path"
      :path="path"
      :root="root"
      @navigate="(p) => emit('navigate', p)"
    />

    <!-- folder list -->
    <div
      class="relative max-h-72 overflow-y-auto rounded-lg border border-slate-200 dark:border-slate-700"
    >
      <div
        v-if="loading"
        class="absolute inset-0 z-10 flex items-center justify-center bg-white/60 dark:bg-surface-deep/60"
        role="status"
      >
        <Spinner>{{ t("common.loading") }}</Spinner>
      </div>

      <ul
        v-if="entries.length"
        class="divide-y divide-slate-100 dark:divide-slate-800"
      >
        <li v-for="e in entries" :key="e.path">
          <button
            class="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left text-sm transition-colors"
            :class="
              e.path === selected
                ? 'bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-200'
                : 'text-slate-700 hover:bg-slate-50 dark:text-slate-200 dark:hover:bg-slate-800/60'
            "
            @click="emit('navigate', e.path)"
            @dblclick="emit('inspect', e.path)"
          >
            <IconFolder class="shrink-0 text-brand-500 dark:text-brand-300" />
            <span class="min-w-0 grow truncate">{{ e.name }}</span>
            <IconChevronRight class="shrink-0 text-slate-400" />
          </button>
        </li>
      </ul>

      <p
        v-else-if="!loading"
        class="px-3 py-6 text-center text-sm text-slate-400"
      >
        {{ t("import.noFolders") }}
      </p>
    </div>

    <p v-if="error" class="text-sm text-danger">{{ error }}</p>
  </div>
</template>
