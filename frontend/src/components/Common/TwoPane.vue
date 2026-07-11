<script setup lang="ts">
import { computed } from "vue";

// Responsive two-column layout: stacks vertically below `breakpoint`, splits side-by-side above it.
// The core anti-scroll primitive — replaces the hand-rolled `lg:grid-cols-2` blocks across the app.
const props = withDefaults(
  defineProps<{
    // 'even' = two equal columns; 'main-aside' = flexible main + a fixed ~22rem aside on the right.
    split?: "even" | "main-aside";
    breakpoint?: "lg" | "xl";
    // Pin the aside to the viewport (with its own inner scroll) on wide screens. Only engages at the
    // breakpoint, where the mobile top bar is hidden, so a fixed top-4 offset is correct.
    stickyAside?: boolean;
  }>(),
  { split: "even", breakpoint: "lg", stickyAside: false },
);

// Complete literal classes (JIT-safe), keyed by prop values.
const GRID = {
  lg: {
    even: "lg:grid-cols-2 lg:items-start",
    "main-aside": "lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start",
  },
  xl: {
    even: "xl:grid-cols-2 xl:items-start",
    "main-aside": "xl:grid-cols-[minmax(0,1fr)_22rem] xl:items-start",
  },
} as const;
const STICKY = {
  lg: "lg:sticky lg:top-4 lg:max-h-[calc(100vh-2rem)] lg:self-start lg:overflow-y-auto",
  xl: "xl:sticky xl:top-4 xl:max-h-[calc(100vh-2rem)] xl:self-start xl:overflow-y-auto",
} as const;

const gridClass = computed(() => GRID[props.breakpoint][props.split]);
const asideClass = computed(() =>
  props.stickyAside ? STICKY[props.breakpoint] : "",
);
</script>

<template>
  <div class="grid gap-4" :class="gridClass">
    <div class="min-w-0">
      <slot name="main" />
    </div>
    <div class="min-w-0" :class="asideClass">
      <slot name="aside" />
    </div>
  </div>
</template>
