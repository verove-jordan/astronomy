<script setup lang="ts">
import { bortleColor } from "@/utils/bortle";

// An interactive Bortle 1–9 scale: clicking a swatch sets the maximum acceptable Bortle. Classes at or
// below the threshold read as "included" (full colour); the chosen ceiling carries a ring.
defineProps<{ modelValue: number }>();
const emit = defineEmits<{ "update:modelValue": [n: number] }>();
const classes = [1, 2, 3, 4, 5, 6, 7, 8, 9];
</script>

<template>
  <div class="flex items-center gap-1">
    <button
      v-for="n in classes"
      :key="n"
      type="button"
      class="h-7 w-7 rounded text-xs font-semibold transition"
      :class="[
        n <= modelValue ? '' : 'opacity-40 hover:opacity-80',
        n === modelValue ? 'ring-2 ring-brand-300' : '',
      ]"
      :style="{
        backgroundColor: bortleColor(n),
        color: n >= 8 ? '#111' : '#fff',
      }"
      :aria-label="`Bortle ${n}`"
      :aria-pressed="n === modelValue"
      @click="emit('update:modelValue', n)"
    >
      {{ n }}
    </button>
  </div>
</template>
