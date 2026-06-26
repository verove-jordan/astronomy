<script setup lang="ts">
defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    :stroke="color || 'currentColor'"
    stroke-width="2"
    stroke-linecap="round"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <path d="M5 12h14" />
  </svg>
</template>
