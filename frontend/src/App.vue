<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { setLocale } from '@/i18n'
import { btnGhost } from '@/constants/styles'
import IconSun from '@/components/Icons/IconSun.vue'
import IconMoon from '@/components/Icons/IconMoon.vue'

const { t, locale } = useI18n()
const isDark = ref(true)

onMounted(() => {
  isDark.value = localStorage.getItem('theme') !== 'light'
  applyTheme()
})

function applyTheme() {
  document.documentElement.classList.toggle('dark', isDark.value)
}
function toggleDark() {
  isDark.value = !isDark.value
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
  applyTheme()
}
function toggleLocale() {
  setLocale(locale.value === 'en' ? 'fr' : 'en')
}

const linkBase =
  'rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700'
const linkActive = 'rounded-md px-3 py-2 text-sm font-medium bg-brand-600 text-white'
const navClass = (active: boolean) => (active ? linkActive : linkBase)

const links = [
  { to: '/import', key: 'nav.import' },
  { to: '/jobs', key: 'nav.jobs' },
  { to: '/library', key: 'nav.library' },
]
</script>

<template>
  <div class="min-h-screen">
    <header class="border-b border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-800">
      <div class="mx-auto flex max-w-7xl items-center gap-4 px-4 py-3">
        <div class="mr-2">
          <div class="text-lg font-bold text-brand-600 dark:text-brand-300">{{ t('app.title') }}</div>
          <div class="hidden text-xs text-slate-500 sm:block dark:text-slate-400">{{ t('app.subtitle') }}</div>
        </div>
        <nav class="flex gap-1">
          <router-link
            v-for="l in links"
            :key="l.to"
            v-slot="{ isActive, href, navigate }"
            :to="l.to"
            custom
          >
            <a :href="href" :class="navClass(isActive)" @click="navigate">{{ t(l.key) }}</a>
          </router-link>
        </nav>
        <div class="ml-auto flex items-center gap-2">
          <button :class="btnGhost" @click="toggleLocale">{{ locale.toUpperCase() }}</button>
          <button :class="btnGhost" aria-label="Toggle theme" @click="toggleDark">
            <IconSun v-if="isDark" />
            <IconMoon v-else />
          </button>
        </div>
      </div>
    </header>
    <main class="mx-auto max-w-7xl px-4 py-6">
      <router-view />
    </main>
  </div>
</template>
