<script setup lang="ts">
defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    width="20"
    height="20"
    fill="none"
    :stroke="color || 'currentColor'"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
  >
    <path d="M3.6 13.2 14.2 8l1.6 3.3-10.6 5.2z" />
    <path d="m15.2 7.6 3.1-1.5 1.6 3.3-3.1 1.5" />
    <path d="M9 15v4" />
    <path d="M6.4 21h5.2" />
  </svg>
</template>
