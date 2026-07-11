<script setup lang="ts">
// Compact, dismissible environment-warnings panel (GET /api/environment): collapsed to an amber
// count chip by default; click to expand the run-impacting problems the tool probes found. Renders
// nothing when the environment is clean or the user dismissed it (session-wide, via the store).
import { ref, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useEnvironmentStore } from "@/stores/environment";

const { t } = useI18n();
const env = useEnvironmentStore();
const expanded = ref(false);
onMounted(() => void env.load());
</script>

<template>
  <div v-if="!env.dismissed && env.warnings.length">
    <!-- Collapsed: an unobtrusive count chip -->
    <button
      v-if="!expanded"
      type="button"
      class="inline-flex items-center gap-1 rounded-full bg-amber-100 px-2.5 py-1 text-xs font-medium text-amber-800 transition-colors hover:bg-amber-200 dark:bg-amber-900/40 dark:text-amber-300 dark:hover:bg-amber-900/60"
      :title="t('env.title')"
      :aria-expanded="expanded"
      @click="expanded = true"
    >
      ⚠ {{ env.warnings.length }}
      <span class="sr-only">{{ t("env.title") }}</span>
    </button>

    <!-- Expanded: the amber panel listing each warning, with a dismiss -->
    <div
      v-else
      class="rounded-md border border-amber-300 bg-amber-50 p-3 dark:border-amber-800/60 dark:bg-amber-900/20"
    >
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="min-w-0 grow truncate text-left text-sm font-medium text-amber-800 dark:text-amber-300"
          :aria-expanded="expanded"
          @click="expanded = false"
        >
          ⚠ {{ t("env.title") }} ({{ env.warnings.length }})
        </button>
        <button
          type="button"
          class="shrink-0 rounded p-0.5 text-amber-700 transition-colors hover:bg-amber-200 dark:text-amber-300 dark:hover:bg-amber-900/50"
          :aria-label="t('env.dismiss')"
          :title="t('env.dismiss')"
          @click="env.dismissed = true"
        >
          ✕
        </button>
      </div>
      <ul class="mt-2 space-y-1">
        <li
          v-for="(w, i) in env.warnings"
          :key="i"
          class="text-xs text-amber-800 dark:text-amber-200"
        >
          · {{ w }}
        </li>
      </ul>
    </div>
  </div>
</template>
