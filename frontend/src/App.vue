<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { setLocale } from "@/i18n";
import { btnGhost } from "@/constants/styles";
import AppLogo from "@/components/Common/AppLogo.vue";
import NightSky from "@/components/Common/NightSky.vue";

// AstroStack is always dark (night-sky tool); the dark class is forced in index.html.
const { t, locale } = useI18n();

function toggleLocale() {
  setLocale(locale.value === "en" ? "fr" : "en");
}

const linkBase =
  "rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700";
const linkActive =
  "rounded-md px-3 py-2 text-sm font-medium bg-brand-600 text-white";
const navClass = (active: boolean) => (active ? linkActive : linkBase);

const links = [
  { to: "/import", key: "nav.import" },
  { to: "/jobs", key: "nav.jobs" },
  { to: "/runs", key: "nav.runs" },
  { to: "/library", key: "nav.library" },
];
</script>

<template>
  <div class="min-h-screen bg-surface-deep">
    <NightSky :dark="true" />
    <header class="relative z-10 border-b border-slate-700 bg-surface-raised">
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
            v-slot="{ isActive, href, navigate }"
            :to="l.to"
            custom
          >
            <a :href="href" :class="navClass(isActive)" @click="navigate">{{
              t(l.key)
            }}</a>
          </router-link>
        </nav>
        <div class="ml-auto flex items-center gap-2">
          <button :class="btnGhost" @click="toggleLocale">
            {{ locale.toUpperCase() }}
          </button>
        </div>
      </div>
    </header>
    <main class="relative z-10 mx-auto max-w-7xl overflow-x-hidden px-4 py-6">
      <router-view />
    </main>
  </div>
</template>
