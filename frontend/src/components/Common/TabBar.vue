<script setup lang="ts">
import { segWrap, segBtn, segActive, segIdle } from "@/constants/styles";

// Reusable page-tab strip. Presentational only: the parent owns the active key + selection. It is
// teleported into the App shell's sticky #page-tabs band (see App.vue), so it renders centered and
// pinned right below the topbar on every tabbed page.
defineProps<{ tabs: { key: string; label: string }[]; active: string }>();
defineEmits<{ select: [key: string] }>();
</script>

<template>
  <div class="border-b border-slate-700 bg-surface-raised">
    <div class="mx-auto flex max-w-7xl justify-center px-4 py-2">
      <div :class="segWrap" role="tablist">
        <!-- data-demo makes each tab addressable by key. Only one TabBar is mounted at a time (it is
             teleported into the shell's #page-tabs band), so the key alone is unambiguous — which is
             what lets the tour-screenshot generator photograph a page's OTHER tabs. -->
        <button
          v-for="tabItem in tabs"
          :key="tabItem.key"
          type="button"
          role="tab"
          :aria-selected="tabItem.key === active"
          :data-demo="`tab-${tabItem.key}`"
          :class="[segBtn, tabItem.key === active ? segActive : segIdle]"
          @click="$emit('select', tabItem.key)"
        >
          {{ tabItem.label }}
        </button>
      </div>
    </div>
  </div>
</template>
