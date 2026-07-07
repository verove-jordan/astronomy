<script setup lang="ts">
// Compact key/value stat cluster — many small readouts packed into little height. Replaces the
// copy-pasted dense stat grids across panels (target detail, image stats, job progress, …).
withDefaults(
  defineProps<{
    items: { label: string; value: string | number; hint?: string }[];
    cols?: 2 | 3 | 4;
  }>(),
  { cols: 4 },
);

// Complete literal classes (JIT-safe), keyed by the column count.
const COLS = {
  2: "grid-cols-2",
  3: "grid-cols-2 sm:grid-cols-3",
  4: "grid-cols-2 sm:grid-cols-4",
} as const;
</script>

<template>
  <dl class="grid gap-x-6 gap-y-2 tabular-nums" :class="COLS[cols]">
    <div v-for="it in items" :key="it.label" class="min-w-0">
      <dt class="truncate text-xs text-slate-400" :title="it.hint || it.label">
        {{ it.label }}
      </dt>
      <dd class="truncate text-sm font-medium text-slate-100">
        {{ it.value }}
      </dd>
    </div>
  </dl>
</template>
