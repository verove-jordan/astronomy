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
    <circle cx="12" cy="9" r="3.5" />
    <path d="M4 15h16M6 18.5h12M8 5.5h8" />
  </svg>
</template>
