<script setup lang="ts">
import { ref } from "vue";
import { useNightSky } from "@/composables/useNightSky";

// Decorative full-viewport night-sky behind the app shell. Adapts to the active theme.
const props = defineProps<{ dark: boolean }>();
const canvas = ref<HTMLCanvasElement | null>(null);
useNightSky(canvas, () => props.dark);
</script>

<template>
  <canvas
    ref="canvas"
    class="pointer-events-none fixed inset-0 z-0 block h-full w-full"
    aria-hidden="true"
  />
</template>
