<script setup lang="ts">
import { ref } from "vue";
import IconChevronDown from "@/components/Icons/IconChevronDown.vue";
import { card } from "@/constants/styles";

// Shared collapsible section: a card with a header button that toggles its content. Open/closed state
// is optionally persisted in localStorage under `storageKey`.
const props = withDefaults(
  defineProps<{ title: string; storageKey?: string; defaultOpen?: boolean }>(),
  { defaultOpen: true },
);

function initialOpen(): boolean {
  if (props.storageKey) {
    const v = localStorage.getItem(props.storageKey);
    if (v !== null) return v === "1";
  }
  return props.defaultOpen;
}
const open = ref(initialOpen());

function toggle() {
  open.value = !open.value;
  if (props.storageKey) {
    try {
      localStorage.setItem(props.storageKey, open.value ? "1" : "0");
    } catch {
      // ignore quota / private-mode errors
    }
  }
}
</script>

<template>
  <section :class="card">
    <button
      type="button"
      class="flex w-full items-center justify-between gap-2 text-left"
      :aria-expanded="open"
      @click="toggle"
    >
      <span
        class="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
      >
        {{ title }}
      </span>
      <IconChevronDown
        class="shrink-0 text-slate-400 transition-transform motion-safe:duration-200"
        :class="open ? '' : '-rotate-90'"
      />
    </button>
    <div v-show="open" class="mt-3">
      <slot />
    </div>
  </section>
</template>
