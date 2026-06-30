<script setup lang="ts">
defineProps<{ color?: string }>();
</script>

<template>
  <!-- A telescope eyepiece: wide eye-lens housing on top, chrome barrel below with a safety groove. -->
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
    <rect x="4" y="3" width="16" height="5" rx="1.5" />
    <path d="M8 8v9a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1V8" />
    <line x1="8" y1="14" x2="16" y2="14" />
  </svg>
</template>
