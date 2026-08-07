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
    <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H19v14H5.5A1.5 1.5 0 0 0 4 19.5z" />
    <path d="M4 19.5A1.5 1.5 0 0 0 5.5 21H19" />
    <path d="M8.5 8.5h6" />
    <path d="M8.5 12h4" />
  </svg>
</template>
