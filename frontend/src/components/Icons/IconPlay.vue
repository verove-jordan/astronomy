<script setup lang="ts">
defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    :fill="color || 'currentColor'"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <path d="M7 4.5 19 12 7 19.5z" />
  </svg>
</template>
