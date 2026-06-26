<script setup lang="ts">
import Pill from "@/components/Common/Pill.vue";
import { filterChipClass } from "@/constants/styles";

// FilterChip always renders the filter LETTER (color is a secondary cue — color-blind safe).
const props = defineProps<{ filter?: string }>();
</script>

<template>
  <Pill :color-class="filterChipClass(props.filter)">
    <span class="font-semibold">{{ props.filter || "—" }}</span>
    <slot />
  </Pill>
</template>
