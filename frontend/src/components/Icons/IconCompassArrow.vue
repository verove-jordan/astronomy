<script setup lang="ts">
// A north-pointing arrow/needle; the consumer rotates it by the star's azimuth to show which way to
// look. Color comes from the consumer (currentColor by default), per the house icon convention.
defineProps<{ color?: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    :fill="color || 'currentColor'"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <path d="M12 2 17 20 12 16 7 20 Z" />
  </svg>
</template>
