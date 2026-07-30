<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { input } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";
import type { SkySearchResult } from "@/types";

// Free-text object picker for the mosaic planner: type-ahead over the WHOLE catalogue via
// GET /api/sky/search, so a target that is below the horizon tonight (or simply outside Tonight's
// ranked list) can still be planned. Picking a hit seeds the draft with the object's catalogued
// size and position angle, so the first grid is already correct.
const SEARCH_DEBOUNCE_MS = 200;

const { t } = useI18n();
const store = useMosaicStore();

const query = ref("");
const open = ref(false);
const highlighted = ref(-1);
const root = ref<HTMLElement | null>(null);
let timer: number | undefined;

watch(query, (q) => {
  window.clearTimeout(timer);
  highlighted.value = -1;
  if (!q.trim()) {
    open.value = false;
    void store.searchTargets("");
    return;
  }
  timer = window.setTimeout(() => {
    open.value = true;
    void store.searchTargets(q);
  }, SEARCH_DEBOUNCE_MS);
});

function pick(res: SkySearchResult) {
  store.seedFromSearch(res);
  query.value = "";
  open.value = false;
  void store.searchTargets("");
}

function move(delta: number) {
  const n = store.searchResults.length;
  if (!n) return;
  open.value = true;
  highlighted.value = (highlighted.value + delta + n) % n;
}

function confirm() {
  const res = store.searchResults[highlighted.value] ?? store.searchResults[0];
  if (res) pick(res);
}

// A hit's secondary line: the common name if the catalogue has one, else the aliases — enough to
// tell NGC 7000 (North America) from its neighbours at a glance.
function subtitle(res: SkySearchResult): string {
  const parts: string[] = [];
  if (res.common_names?.length) parts.push(res.common_names[0]);
  else if (res.aliases?.length) parts.push(res.aliases.slice(0, 2).join(" · "));
  if (res.type) parts.push(res.type);
  if (res.size_arcmin) parts.push(`${res.size_arcmin.toFixed(0)}′`);
  if (res.mag !== undefined) parts.push(`mag ${res.mag.toFixed(1)}`);
  return parts.join(" · ");
}

function onDocumentPointerDown(e: PointerEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false;
}
document.addEventListener("pointerdown", onDocumentPointerDown);
onBeforeUnmount(() => {
  window.clearTimeout(timer);
  document.removeEventListener("pointerdown", onDocumentPointerDown);
});
</script>

<template>
  <div ref="root" class="relative">
    <input
      v-model="query"
      :class="input"
      type="search"
      autocomplete="off"
      :placeholder="t('mosaic.search.placeholder')"
      :aria-label="t('mosaic.search.placeholder')"
      @focus="query.trim() && (open = true)"
      @keydown.down.prevent="move(1)"
      @keydown.up.prevent="move(-1)"
      @keydown.enter.prevent="confirm"
      @keydown.esc="open = false"
    />
    <div
      v-if="open"
      class="absolute z-30 mt-1 max-h-72 w-full overflow-y-auto rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-700 dark:bg-slate-800"
    >
      <p
        v-if="store.searchLoading && !store.searchResults.length"
        class="px-3 py-2 text-xs text-slate-400"
      >
        {{ t("mosaic.search.searching") }}
      </p>
      <p
        v-else-if="!store.searchResults.length"
        class="px-3 py-2 text-xs text-slate-400"
      >
        {{ t("mosaic.search.noResults") }}
      </p>
      <button
        v-for="(res, i) in store.searchResults"
        :key="res.name"
        class="block w-full px-3 py-2 text-left hover:bg-slate-100 dark:hover:bg-slate-700"
        :class="i === highlighted ? 'bg-slate-100 dark:bg-slate-700' : ''"
        @click="pick(res)"
        @mouseenter="highlighted = i"
      >
        <span class="text-sm font-medium text-slate-700 dark:text-slate-100">{{
          res.name
        }}</span>
        <span class="ml-2 text-xs text-slate-400">{{ subtitle(res) }}</span>
      </button>
    </div>
  </div>
</template>
