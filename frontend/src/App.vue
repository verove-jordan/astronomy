<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { setLocale } from "@/i18n";
import { btnGhost } from "@/constants/styles";
import AppLogo from "@/components/Common/AppLogo.vue";
import NightSky from "@/components/Common/NightSky.vue";
import FileViewer from "@/components/Common/FileViewer.vue";
import { useViewerStore } from "@/stores/viewer";
import { useAgentStore } from "@/stores/agent";
import { useAutoRefresh } from "@/composables/useAutoRefresh";

// AstroStack is always dark (night-sky tool); the dark class is forced in index.html.
const { t, locale } = useI18n();
// One app-wide file viewer (opened from any file table / the inspector via the viewer store).
const viewer = useViewerStore();
const route = useRoute();

// Poll local-agent availability app-wide so the "AstroAgent" nav link (and page) show only while the
// local vision model server is running.
const agent = useAgentStore();
const { enabled: agentPoll } = useAutoRefresh(
  () => agent.refreshStatus(),
  10_000,
);

function toggleLocale() {
  setLocale(locale.value === "en" ? "fr" : "en");
}

const linkBase =
  "rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700";
const linkActive =
  "rounded-md px-3 py-2 text-sm font-medium bg-brand-600 text-white";
const navClass = (active: boolean) => (active ? linkActive : linkBase);

// Top-level destinations. "Processing" stays highlighted for any /processing/* route (its nested
// Import/Live/Tasks/Runs/Library tabs, plus the old-path redirects that land there). The AstroAgent
// link is appended only while the local model server is up.
const links = computed(() => {
  const base: { to: string; key: string; prefix?: string }[] = [
    { to: "/tonight", key: "nav.tonight" },
    { to: "/goto", key: "nav.goto" },
    { to: "/calendar", key: "nav.calendar" },
    { to: "/processing", key: "nav.processing", prefix: "/processing" },
  ];
  if (agent.available) base.push({ to: "/astroagent", key: "nav.astroAgent" });
  return base;
});
function linkIsActive(l: { to: string; prefix?: string }): boolean {
  return l.prefix ? route.path.startsWith(l.prefix) : route.path === l.to;
}

// The page-tab band (#page-tabs) is sticky just below the topbar, whose height is dynamic (nav wraps,
// the subtitle hides on small screens). Track it in a CSS var so the band stays flush on any layout.
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
  <div ref="rootEl" class="min-h-screen bg-surface-deep">
    <NightSky :dark="true" />
    <header
      ref="headerEl"
      class="sticky top-0 z-30 border-b border-slate-700 bg-surface-raised"
    >
      <div class="mx-auto flex min-w-0 max-w-7xl items-center gap-4 px-4 py-3">
        <div class="flex min-w-0 items-center gap-2">
          <AppLogo />
          <div class="min-w-0">
            <div class="truncate text-lg font-bold text-brand-300">
              {{ t("app.title") }}
            </div>
            <div class="hidden truncate text-xs text-slate-400 sm:block">
              {{ t("app.subtitle") }}
            </div>
          </div>
        </div>
        <nav class="flex min-w-0 flex-wrap gap-1">
          <router-link
            v-for="l in links"
            :key="l.to"
            v-slot="{ href, navigate }"
            :to="l.to"
            custom
          >
            <a
              :href="href"
              :class="navClass(linkIsActive(l))"
              @click="navigate"
              >{{ t(l.key) }}</a
            >
          </router-link>
        </nav>
        <div class="ml-auto flex items-center gap-2">
          <button :class="btnGhost" @click="toggleLocale">
            {{ locale.toUpperCase() }}
          </button>
        </div>
      </div>
    </header>

    <!-- Sticky page-tab band: a sibling between header and main so it pins to the viewport (a sticky
         element inside <main> would fail — main's overflow-x-hidden makes it a scroll container). Tabbed
         views Teleport their <TabBar> here. -->
    <div
      id="page-tabs"
      class="sticky z-20"
      :style="{ top: 'var(--topbar-h, 3.5rem)' }"
    />

    <main class="relative z-10 mx-auto max-w-7xl overflow-x-hidden px-4 py-6">
      <router-view />
    </main>

    <FileViewer
      v-if="viewer.path"
      :path="viewer.path"
      @close="viewer.close()"
    />
  </div>
</template>
