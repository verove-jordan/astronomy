<script setup lang="ts">
defineProps<{ color?: string; exit?: boolean }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    :stroke="color || 'currentColor'"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <!-- Four corner brackets pointing outward to enter, inward to leave. -->
    <template v-if="exit">
      <path d="M9 4v5H4M15 4v5h5M9 20v-5H4M15 20v-5h5" />
    </template>
    <template v-else>
      <path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" />
    </template>
  </svg>
</template>
