<script setup lang="ts" generic="T extends Record<string, unknown>">
import { reactive, ref, computed, nextTick } from "vue";
import { th, td, input } from "@/constants/styles";

export interface Column<R> {
  key: string;
  label: string;
  sortable?: boolean;
  searchable?: boolean;
  align?: "left" | "right" | "center";
  format?: (value: unknown, row: R) => string;
}

const props = defineProps<{
  columns: Column<T>[];
  rows: T[];
  rowClass?: (row: T) => string;
  rowKey?: (row: T) => string | number; // stable per-row id; enables scrollToKey + row-click correlation
  maxHeight?: string; // when set, the body scrolls vertically within this height and the header sticks
}>();

const emit = defineEmits<{ "row-click": [row: T] }>();

const rootRef = ref<HTMLElement | null>(null);

// Scroll the row carrying this rowKey into view, but only when it is off-screen (block: "nearest"). Lets a
// parent reveal a row it selected elsewhere (e.g. by clicking a linked map marker). No-op without rowKey.
function scrollToKey(key: string | number) {
  nextTick(() => {
    const el = rootRef.value?.querySelector<HTMLElement>(
      `[data-row-key="${CSS.escape(String(key))}"]`,
    );
    el?.scrollIntoView({ block: "nearest" });
  });
}
defineExpose({ scrollToKey });

interface SortKey {
  key: string;
  dir: 1 | -1;
}

const sortKeys = ref<SortKey[]>([]);
const search = reactive<Record<string, string>>({});

const anySearchable = computed(() => props.columns.some((c) => c.searchable));

function toggleSort(col: Column<T>, shift: boolean) {
  if (!col.sortable) return;
  const existing = sortKeys.value.find((s) => s.key === col.key);
  if (shift) {
    if (existing) existing.dir = existing.dir === 1 ? -1 : 1;
    else sortKeys.value.push({ key: col.key, dir: 1 });
  } else if (existing && sortKeys.value.length === 1) {
    existing.dir = existing.dir === 1 ? -1 : 1;
  } else {
    sortKeys.value = [{ key: col.key, dir: 1 }];
  }
}

function sortIndicator(key: string): string {
  const idx = sortKeys.value.findIndex((s) => s.key === key);
  if (idx === -1) return "";
  const arrow = sortKeys.value[idx].dir === 1 ? "▲" : "▼";
  return sortKeys.value.length > 1 ? `${arrow}${idx + 1}` : arrow;
}

const processed = computed<T[]>(() => {
  let out = props.rows.filter((row) =>
    props.columns.every((c) => {
      if (!c.searchable) return true;
      const q = (search[c.key] || "").toLowerCase();
      if (!q) return true;
      // Search the DISPLAYED value (applies col.format) so translated labels match in any locale.
      return display(c, row).toLowerCase().includes(q);
    }),
  );
  if (sortKeys.value.length) {
    out = [...out].sort((a, b) => {
      for (const s of sortKeys.value) {
        const av = a[s.key];
        const bv = b[s.key];
        let cmp = 0;
        if (typeof av === "number" && typeof bv === "number") cmp = av - bv;
        else cmp = String(av ?? "").localeCompare(String(bv ?? ""));
        if (cmp !== 0) return cmp * s.dir;
      }
      return 0;
    });
  }
  return out;
});

function display(col: Column<T>, row: T): string {
  if (col.format) return col.format(row[col.key], row);
  const v = row[col.key];
  return v === undefined || v === null ? "" : String(v);
}
</script>

<template>
  <div
    ref="rootRef"
    :class="[
      'rounded-lg border border-slate-200 dark:border-slate-700',
      maxHeight ? 'overflow-auto' : 'overflow-x-auto',
    ]"
    :style="maxHeight ? { maxHeight } : undefined"
  >
    <table class="min-w-full divide-y divide-slate-200 dark:divide-slate-700">
      <thead
        :class="[
          'bg-slate-100 dark:bg-slate-800',
          maxHeight ? 'sticky top-0 z-10' : '',
        ]"
      >
        <tr>
          <th
            v-for="col in columns"
            :key="col.key"
            :class="[
              th,
              col.align === 'right'
                ? 'text-right'
                : col.align === 'center'
                  ? 'text-center'
                  : 'text-left',
            ]"
            @click="(e) => toggleSort(col, e.shiftKey)"
          >
            {{ col.label }}
            <span v-if="col.sortable" class="ml-1 text-brand-500">{{
              sortIndicator(col.key)
            }}</span>
          </th>
        </tr>
        <tr v-if="anySearchable" class="bg-slate-50 dark:bg-brand-900/10">
          <th v-for="col in columns" :key="col.key" class="px-2 py-1">
            <input
              v-if="col.searchable"
              v-model="search[col.key]"
              :class="input"
              :placeholder="'⌕'"
              type="text"
            />
          </th>
        </tr>
      </thead>
      <tbody class="divide-y divide-slate-100 dark:divide-slate-800">
        <tr
          v-for="(row, i) in processed"
          :key="rowKey ? rowKey(row) : i"
          :data-row-key="rowKey ? rowKey(row) : i"
          :class="
            rowClass
              ? rowClass(row)
              : 'hover:bg-slate-50 dark:hover:bg-slate-800/50'
          "
          @click="emit('row-click', row)"
        >
          <td
            v-for="col in columns"
            :key="col.key"
            :class="[
              td,
              col.align === 'right'
                ? 'text-right'
                : col.align === 'center'
                  ? 'text-center'
                  : 'text-left',
            ]"
          >
            <slot :name="'cell-' + col.key" :row="row" :value="row[col.key]">
              {{ display(col, row) }}
            </slot>
          </td>
        </tr>
        <tr v-if="processed.length === 0">
          <td :class="td" :colspan="columns.length">
            <span class="text-slate-400">—</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
