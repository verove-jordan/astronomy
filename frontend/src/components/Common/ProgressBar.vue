<script setup lang="ts">
// percent: 0–100 fill. barClass: optional complete (JIT-safe) Tailwind fill color; defaults to brand.
// active: when true, sweep a shimmer across the fill so a running job still reads as "working" even
// while the bar sits near-full during a long final step (the bar only reaches 100% once truly done).
defineProps<{ percent: number; barClass?: string; active?: boolean }>();
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
      class="relative h-full overflow-hidden rounded-full transition-all"
      :class="barClass || 'bg-brand-500'"
      :style="{ width: percent + '%' }"
    >
      <div
        v-if="active"
        class="pointer-events-none absolute inset-0 bg-gradient-to-r from-transparent via-white/30 to-transparent bg-[length:200%_100%] motion-safe:animate-[shimmer_1.4s_ease-in-out_infinite]"
        aria-hidden="true"
      />
    </div>
  </div>
</template>
