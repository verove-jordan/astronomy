<script setup lang="ts">
import { usePathBreadcrumb } from "@/composables/usePathBreadcrumb";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";

const props = defineProps<{ path: string; root: string }>();
const emit = defineEmits<{ navigate: [path: string] }>();
const crumbs = usePathBreadcrumb(
  () => props.path,
  () => props.root,
);
</script>

<template>
  <nav
    aria-label="Breadcrumb"
    class="flex min-w-0 flex-wrap items-center gap-0.5 text-xs text-slate-500 dark:text-slate-400"
  >
    <template v-for="(c, i) in crumbs" :key="c.path">
      <IconChevronRight
        v-if="i > 0"
        class="text-slate-300 dark:text-slate-600"
      />
      <button
        class="max-w-[12rem] truncate rounded px-1.5 py-0.5 hover:bg-slate-100 hover:text-brand-600 dark:hover:bg-slate-700 dark:hover:text-brand-300"
        :class="
          i === crumbs.length - 1
            ? 'font-medium text-slate-700 dark:text-slate-200'
            : ''
        "
        @click="emit('navigate', c.path)"
      >
        {{ c.label }}
      </button>
    </template>
  </nav>
</template>
