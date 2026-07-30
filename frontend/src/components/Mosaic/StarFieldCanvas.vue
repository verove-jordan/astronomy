<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useMosaicStore } from "@/stores/mosaic";
import {
  useStarFieldCanvas,
  type StarFieldRect,
} from "@/composables/useStarFieldCanvas";
import type { StarfieldStar } from "@/types";

// A finder chart: deepstars fetched (and cached) through the mosaic store, frame rectangles drawn
// by useStarFieldCanvas. Height comes from the parent via the class attribute (fall-through).
const props = defineProps<{
  centerRaDeg: number;
  centerDecDeg: number;
  fovDeg: number;
  rects: StarFieldRect[];
  mirrored?: boolean;
}>();

const store = useMosaicStore();
const canvas = ref<HTMLCanvasElement | null>(null);
const stars = ref<StarfieldStar[]>([]);
const failed = ref(false);

async function loadStars() {
  try {
    failed.value = false;
    stars.value = await store.fetchStarfield(
      props.centerRaDeg,
      props.centerDecDeg,
      props.fovDeg,
    );
  } catch {
    failed.value = true;
    stars.value = [];
  }
}
watch(() => [props.centerRaDeg, props.centerDecDeg, props.fovDeg], loadStars, {
  immediate: true,
});

const opts = computed(() => ({
  centerRaDeg: props.centerRaDeg,
  centerDecDeg: props.centerDecDeg,
  fovDeg: props.fovDeg,
  stars: stars.value,
  rects: props.rects,
  mirrored: props.mirrored,
}));
useStarFieldCanvas(canvas, opts);
</script>

<template>
  <div class="relative">
    <canvas ref="canvas" class="h-full w-full rounded-md" />
    <p
      v-if="failed"
      class="absolute inset-x-0 bottom-1 text-center text-[11px] text-slate-400"
    >
      ⚠
    </p>
  </div>
</template>
