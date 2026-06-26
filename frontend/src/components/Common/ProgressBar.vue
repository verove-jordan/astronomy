<script setup lang="ts">
defineProps<{ percent: number }>();
</script>

<template>
  <div
    class="h-2 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700"
    role="progressbar"
    :aria-valuenow="percent"
    aria-valuemin="0"
    aria-valuemax="100"
  >
    <div
      class="h-full rounded-full bg-brand-500 transition-all"
      :style="{ width: percent + '%' }"
    />
  </div>
</template>
