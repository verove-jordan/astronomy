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
    class="h-4 w-4"
    aria-hidden="true"
  >
    <path d="M5 13l1.6-4.3A2 2 0 0 1 8.5 7.4h7a2 2 0 0 1 1.9 1.3L19 13" />
    <path d="M3 13.5h18V17a1 1 0 0 1-1 1h-1.2M6.2 18H4a1 1 0 0 1-1-1z" />
    <circle cx="7.5" cy="17.5" r="1.6" />
    <circle cx="16.5" cy="17.5" r="1.6" />
  </svg>
</template>
