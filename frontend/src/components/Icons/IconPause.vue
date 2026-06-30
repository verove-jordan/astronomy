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
    <path d="M7 5h3v14H7zM14 5h3v14h-3z" />
  </svg>
</template>
