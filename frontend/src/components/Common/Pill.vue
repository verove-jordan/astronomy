<script setup lang="ts">
// The single chip/pill primitive. Callers pass a complete (JIT-safe) colorClass string.
defineProps<{ colorClass: string; role?: string }>();
</script>

<template>
  <span
    :class="[
      'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium',
      colorClass,
    ]"
    :role="role"
  >
    <slot />
  </span>
</template>
