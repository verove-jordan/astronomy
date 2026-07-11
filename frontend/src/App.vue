<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import AppLogo from "@/components/Common/AppLogo.vue";
import AppSidebar from "@/components/Common/AppSidebar.vue";
import NightSky from "@/components/Common/NightSky.vue";
import FileViewer from "@/components/Common/FileViewer.vue";
import IconMenu from "@/components/Icons/IconMenu.vue";
import { useViewerStore } from "@/stores/viewer";
import { useAgentStore } from "@/stores/agent";
import { useAutoRefresh } from "@/composables/useAutoRefresh";

// AstroStack is always dark (night-sky tool); the dark class is forced in index.html.
const { t } = useI18n();
// One app-wide file viewer (opened from any file table / the inspector via the viewer store).
const viewer = useViewerStore();

// Poll local-agent availability app-wide so the "AstroAgent" nav link (and page) show only while the
// local vision model server is running.
const agent = useAgentStore();
const { enabled: agentPoll } = useAutoRefresh(
  () => agent.refreshStatus(),
  10_000,
);

// Mobile: the left rail is an off-canvas drawer toggled by the top-bar hamburger.
const drawerOpen = ref(false);

// The page-tab band (#page-tabs) pins just below the top chrome. On lg+ the sidebar owns the chrome and
// the (lg:hidden) mobile bar collapses to 0 height, so the band pins at the very top; on mobile the band
// pins right below the bar. Track the bar's live height in a CSS var so the band stays flush either way.
const rootEl = ref<HTMLElement | null>(null);
const headerEl = ref<HTMLElement | null>(null);
let ro: ResizeObserver | null = null;
function syncTopbarHeight() {
  const h = headerEl.value?.offsetHeight ?? 0;
  rootEl.value?.style.setProperty("--topbar-h", `${h}px`);
}
onMounted(() => {
  syncTopbarHeight();
  ro = new ResizeObserver(syncTopbarHeight);
  if (headerEl.value) ro.observe(headerEl.value);
  void agent.refreshStatus(); // immediate check so the link appears without waiting a full interval
  agentPoll.value = true;
});
onBeforeUnmount(() => {
  ro?.disconnect();
  ro = null;
});
</script>

<template>
  <div ref="rootEl" class="min-h-screen bg-surface-deep lg:flex">
    <NightSky :dark="true" />

    <!-- Mobile drawer backdrop -->
    <div
      v-if="drawerOpen"
      class="fixed inset-0 z-40 bg-black/60 lg:hidden"
      aria-hidden="true"
      @click="drawerOpen = false"
    />

    <!-- Left rail: sticky on desktop, off-canvas drawer on mobile. -->
    <AppSidebar
      class="fixed inset-y-0 left-0 z-50 transition-transform motion-safe:duration-200 lg:sticky lg:top-0 lg:z-30 lg:translate-x-0 lg:self-start"
      :class="
        drawerOpen ? 'translate-x-0' : '-translate-x-full lg:translate-x-0'
      "
      @navigate="drawerOpen = false"
      @close="drawerOpen = false"
    />

    <!-- Content column -->
    <div class="flex min-w-0 flex-1 flex-col">
      <!-- Slim mobile top bar (hamburger + brand); hidden on desktop where the rail owns the chrome. Its
           live height feeds --topbar-h so the sticky page-tab band pins flush below it. -->
      <header
        ref="headerEl"
        class="sticky top-0 z-30 flex items-center gap-3 border-b border-slate-700 bg-surface-raised px-4 py-3 lg:hidden"
      >
        <button
          type="button"
          class="rounded-md p-1 text-slate-300 hover:bg-slate-700 hover:text-white"
          :aria-label="t('app.openMenu')"
          :aria-expanded="drawerOpen"
          @click="drawerOpen = true"
        >
          <IconMenu />
        </button>
        <AppLogo size-class="h-7 w-7" />
        <span class="truncate text-base font-bold text-brand-300">{{
          t("app.title")
        }}</span>
      </header>

      <!-- Sticky page-tab band: a sibling ABOVE <main> (main's overflow-x-hidden makes it a scroll
           container, so a sticky child inside main would fail). Tabbed views Teleport their <TabBar>. -->
      <div
        id="page-tabs"
        class="sticky z-20"
        :style="{ top: 'var(--topbar-h, 0px)' }"
      />

      <main
        class="relative z-10 mx-auto w-full max-w-screen-2xl overflow-x-hidden px-4 py-6 sm:px-6 lg:px-8"
      >
        <router-view />
      </main>
    </div>

    <FileViewer
      v-if="viewer.path"
      :path="viewer.path"
      @close="viewer.close()"
    />
  </div>
</template>
