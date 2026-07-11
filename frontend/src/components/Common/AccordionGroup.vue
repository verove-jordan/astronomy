<script setup lang="ts">
import { ref } from "vue";
import IconChevronDown from "@/components/Icons/IconChevronDown.vue";
import { card } from "@/constants/styles";

// A group of collapsible sections (built on the CollapsibleCard idiom). With `exclusive`, opening one
// closes the others (one-open-at-a-time). Open state is optionally persisted per item under
// `${storageKey}:${item.key}`. Each item renders its content through a same-named slot.
const props = withDefaults(
  defineProps<{
    items: { key: string; title: string }[];
    exclusive?: boolean;
    storageKey?: string;
    defaultOpen?: string[]; // item keys open initially (used only when no persisted state exists)
  }>(),
  { exclusive: false },
);

function persisted(key: string): boolean | null {
  if (!props.storageKey) return null;
  const v = localStorage.getItem(`${props.storageKey}:${key}`);
  return v === null ? null : v === "1";
}
const openSet = ref<Set<string>>(
  new Set(
    props.items
      .filter((it) => {
        const p = persisted(it.key);
        return p === null ? (props.defaultOpen?.includes(it.key) ?? false) : p;
      })
      .map((it) => it.key),
  ),
);

function isOpen(key: string): boolean {
  return openSet.value.has(key);
}
function persist(next: Set<string>) {
  if (!props.storageKey) return;
  try {
    for (const it of props.items)
      localStorage.setItem(
        `${props.storageKey}:${it.key}`,
        next.has(it.key) ? "1" : "0",
      );
  } catch {
    // ignore quota / private-mode errors
  }
}
function toggle(key: string) {
  const next = new Set(openSet.value);
  if (next.has(key)) {
    next.delete(key);
  } else {
    if (props.exclusive) next.clear();
    next.add(key);
  }
  openSet.value = next;
  persist(next);
}
// open expands a section programmatically (e.g. reveal "Browse" when a connection's Browse is clicked).
function open(key: string) {
  if (openSet.value.has(key)) return;
  const next = new Set(openSet.value);
  if (props.exclusive) next.clear();
  next.add(key);
  openSet.value = next;
  persist(next);
}

defineExpose({ open, toggle, isOpen });
</script>

<template>
  <div class="space-y-3">
    <section v-for="it in items" :key="it.key" :class="card">
      <button
        type="button"
        class="flex w-full items-center justify-between gap-2 text-left"
        :aria-expanded="isOpen(it.key)"
        @click="toggle(it.key)"
      >
        <span
          class="text-xs font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400"
        >
          {{ it.title }}
        </span>
        <IconChevronDown
          class="shrink-0 text-slate-400 transition-transform motion-safe:duration-200"
          :class="isOpen(it.key) ? '' : '-rotate-90'"
        />
      </button>
      <div v-show="isOpen(it.key)" class="mt-3">
        <slot :name="it.key" />
      </div>
    </section>
  </div>
</template>
