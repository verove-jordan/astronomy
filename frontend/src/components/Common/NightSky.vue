<script setup lang="ts">
import { ref } from "vue";
import { useRoute } from "vue-router";
import { useNightSky } from "@/composables/useNightSky";

// Decorative full-viewport night-sky behind the app shell. Adapts to the active theme.
const props = defineProps<{ dark: boolean }>();
const canvas = ref<HTMLCanvasElement | null>(null);

// Pause the animation on live-work routes (job detail, live stacking) where the user watches real
// rendering — no reason to spend frames on the decorative background there.
const HEAVY_ROUTES = new Set(["job", "livestack"]);
const route = useRoute();
const active = () => !HEAVY_ROUTES.has(String(route.name));

useNightSky(canvas, () => props.dark, active);
</script>

<template>
  <canvas
    ref="canvas"
    class="pointer-events-none fixed inset-0 z-0 block h-full w-full"
    aria-hidden="true"
  />
</template>
