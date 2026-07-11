<script setup lang="ts">
// A small robot/agent head — the AstroAgent nav entry (the local vision-model assistant). Color comes
// from the consumer (currentColor by default), per the house icon convention.
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
    <rect x="4" y="8" width="16" height="11" rx="2" />
    <path d="M12 4v4M2 13v2M22 13v2M9 13h.01M15 13h.01" />
    <circle cx="12" cy="3" r="1.2" />
  </svg>
</template>
