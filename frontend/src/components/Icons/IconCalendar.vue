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
    stroke-linejoin="round"
    class="h-5 w-5"
    aria-hidden="true"
  >
    <rect x="3" y="4.5" width="18" height="16" rx="2" />
    <path d="M3 9.5h18M8 3v3M16 3v3" />
  </svg>
</template>
