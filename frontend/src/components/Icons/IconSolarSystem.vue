<script setup lang="ts">
// A sun with two orbits round it, one carrying a planet — the orrery. Color from the consumer
// (currentColor by default), per the house icon convention.
const _props = defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    :stroke="_props.color || 'currentColor'"
    stroke-width="1.6"
    stroke-linecap="round"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <circle cx="12" cy="12" r="2.4" />
    <ellipse cx="12" cy="12" rx="9.2" ry="4.4" transform="rotate(-20 12 12)" />
    <ellipse cx="12" cy="12" rx="5.6" ry="2.6" transform="rotate(-20 12 12)" />
    <circle
      cx="19.6"
      cy="9.2"
      r="1.15"
      :fill="_props.color || 'currentColor'"
      stroke="none"
    />
  </svg>
</template>
