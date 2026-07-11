<script setup lang="ts">
import { ref, computed, type Component } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { setLocale } from "@/i18n";
import { useAgentStore } from "@/stores/agent";
import AppLogo from "@/components/Common/AppLogo.vue";
import IconMoon from "@/components/Icons/IconMoon.vue";
import IconCompassArrow from "@/components/Icons/IconCompassArrow.vue";
import IconCalendar from "@/components/Icons/IconCalendar.vue";
import IconCamera from "@/components/Icons/IconCamera.vue";
import IconAgent from "@/components/Icons/IconAgent.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import IconX from "@/components/Icons/IconX.vue";

// The flush-left navigation rail. Presentational chrome only — App.vue owns the responsive positioning
// (sticky on desktop, off-canvas drawer on mobile) via fall-through classes; this component owns the
// nav model, the desktop collapse-to-icons state, and the locale toggle.
const emit = defineEmits<{ navigate: []; close: [] }>();

const { t, locale } = useI18n();
const route = useRoute();
const agent = useAgentStore();

// Top-level destinations. "Processing" stays active for any /processing/* route; the AstroAgent entry is
// appended only while the local model server is up (mirrors the old top-nav behaviour).
type NavLink = { to: string; key: string; prefix?: string; icon: Component };
const links = computed<NavLink[]>(() => {
  const base: NavLink[] = [
    { to: "/tonight", key: "nav.tonight", icon: IconMoon },
    { to: "/goto", key: "nav.goto", icon: IconCompassArrow },
    { to: "/calendar", key: "nav.calendar", icon: IconCalendar },
    {
      to: "/processing",
      key: "nav.processing",
      prefix: "/processing",
      icon: IconCamera,
    },
  ];
  if (agent.available)
    base.push({ to: "/astroagent", key: "nav.astroAgent", icon: IconAgent });
  return base;
});
function isActive(l: NavLink): boolean {
  return l.prefix ? route.path.startsWith(l.prefix) : route.path === l.to;
}
// Navigate, then close the mobile drawer (no-op on desktop where it stays open).
function go(navigate: () => void) {
  navigate();
  emit("navigate");
}

// Desktop collapse-to-icons, persisted. Only affects lg+ (mobile drawer always shows full labels).
const COLLAPSE_KEY = "astro:sidebar-collapsed";
const collapsed = ref(localStorage.getItem(COLLAPSE_KEY) === "1");
function toggleCollapse() {
  collapsed.value = !collapsed.value;
  try {
    localStorage.setItem(COLLAPSE_KEY, collapsed.value ? "1" : "0");
  } catch {
    // ignore quota / private-mode errors
  }
}

function toggleLocale() {
  setLocale(locale.value === "en" ? "fr" : "en");
}

// Complete literal class strings (JIT-safe).
const rowBase =
  "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors";
const rowIdle = "text-slate-300 hover:bg-slate-700/70 hover:text-white";
const rowActive = "bg-brand-600 text-white";
</script>

<template>
  <aside
    :class="[
      'flex h-screen shrink-0 flex-col border-r border-slate-700 bg-surface-raised',
      collapsed ? 'w-72 lg:w-[4.75rem]' : 'w-72 lg:w-60',
    ]"
  >
    <!-- Brand + mobile close -->
    <div class="flex items-center gap-2 px-3 py-4">
      <AppLogo :size-class="collapsed ? 'h-8 w-8 lg:mx-auto' : 'h-8 w-8'" />
      <div class="min-w-0" :class="collapsed ? 'lg:hidden' : ''">
        <div class="truncate text-base font-bold leading-tight text-brand-300">
          {{ t("app.title") }}
        </div>
      </div>
      <button
        type="button"
        class="ml-auto rounded-md p-1 text-slate-400 hover:bg-slate-700 hover:text-white lg:hidden"
        :aria-label="t('app.closeMenu')"
        @click="emit('close')"
      >
        <IconX />
      </button>
    </div>

    <!-- Primary navigation -->
    <nav class="flex-1 space-y-1 overflow-y-auto px-2 py-2">
      <router-link
        v-for="l in links"
        :key="l.to"
        v-slot="{ href, navigate }"
        :to="l.to"
        custom
      >
        <a
          :href="href"
          :class="[
            rowBase,
            isActive(l) ? rowActive : rowIdle,
            collapsed ? 'lg:justify-center' : '',
          ]"
          :aria-current="isActive(l) ? 'page' : undefined"
          :title="collapsed ? t(l.key) : undefined"
          @click="go(navigate)"
        >
          <span class="grid h-5 w-5 shrink-0 place-items-center">
            <component :is="l.icon" />
          </span>
          <span class="truncate" :class="collapsed ? 'lg:hidden' : ''">{{
            t(l.key)
          }}</span>
        </a>
      </router-link>
    </nav>

    <!-- Footer: locale toggle + desktop collapse -->
    <div class="space-y-1 border-t border-slate-700 px-2 py-2">
      <button
        type="button"
        class="w-full"
        :class="[rowBase, rowIdle, collapsed ? 'lg:justify-center' : '']"
        :title="collapsed ? t('app.language') : undefined"
        @click="toggleLocale"
      >
        <span
          class="grid h-5 w-5 shrink-0 place-items-center text-xs font-semibold"
        >
          {{ locale.toUpperCase() }}
        </span>
        <span class="truncate" :class="collapsed ? 'lg:hidden' : ''">{{
          t("app.language")
        }}</span>
      </button>
      <button
        type="button"
        class="hidden w-full lg:flex"
        :class="[rowBase, rowIdle, collapsed ? 'lg:justify-center' : '']"
        :aria-label="collapsed ? t('nav.expand') : t('nav.collapse')"
        @click="toggleCollapse"
      >
        <span class="grid h-5 w-5 shrink-0 place-items-center">
          <IconChevronRight
            class="transition-transform motion-safe:duration-200"
            :class="collapsed ? '' : 'rotate-180'"
          />
        </span>
        <span class="truncate" :class="collapsed ? 'lg:hidden' : ''">{{
          t("nav.collapse")
        }}</span>
      </button>
    </div>
  </aside>
</template>
