<script setup lang="ts">
defineProps<{ color?: string }>();
</script>

<template>
  <!-- "1:1" actual-size glyph; filled text adapts to currentColor / the color prop. -->
  <svg
    viewBox="0 0 24 24"
    :fill="color || 'currentColor'"
    class="h-4 w-4"
    aria-hidden="true"
  >
    <text
      x="12"
      y="16"
      text-anchor="middle"
      font-size="11"
      font-weight="700"
      font-family="ui-monospace, monospace"
    >
      1:1
    </text>
  </svg>
</template>
