<script setup lang="ts">
// Three overlapping tile outlines — the mosaic planner. Color from the consumer (currentColor by
// default), per the house icon convention.
const _props = defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    :stroke="_props.color || 'currentColor'"
    stroke-width="1.8"
    stroke-linejoin="round"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <rect x="3" y="3" width="9.5" height="9.5" rx="1" />
    <rect x="11.5" y="7" width="9.5" height="9.5" rx="1" />
    <rect x="7" y="11.5" width="9.5" height="9.5" rx="1" />
  </svg>
</template>
