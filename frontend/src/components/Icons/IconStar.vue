<script setup lang="ts">
defineProps<{ color?: string; filled?: boolean }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    :fill="filled ? color || 'currentColor' : 'none'"
    :stroke="color || 'currentColor'"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <path
      d="M12 2.5l2.9 5.88 6.49.94-4.7 4.58 1.11 6.46L12 17.3l-5.8 3.06 1.1-6.46-4.69-4.58 6.49-.94L12 2.5z"
    />
  </svg>
</template>
