<script setup lang="ts">
import { onMounted, onBeforeUnmount } from "vue";
import { useI18n } from "vue-i18n";
import IconX from "@/components/Icons/IconX.vue";

// Fullscreen host for the sky map: the panel fills the content area beside the nav rail (App.vue
// measures --rail-w; 0 on mobile where the drawer overlays) and below the mobile top bar
// (--topbar-h; 0 on desktop). Esc and a backdrop click close it — mirrors Common/FileViewer.vue.
const emit = defineEmits<{ (e: "close"): void }>();
const { t } = useI18n();

function onEsc(e: KeyboardEvent) {
  if (e.key === "Escape") emit("close");
}
onMounted(() => window.addEventListener("keydown", onEsc));
onBeforeUnmount(() => window.removeEventListener("keydown", onEsc));
</script>

<template>
  <Teleport to="body">
    <!-- Backdrop dims the whole app (the rail stays visible through it); clicking it closes. -->
    <div
      class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm"
      aria-hidden="true"
      @click="emit('close')"
    />
    <!-- Panel over the content area only; the padding gutter falls through to the backdrop. -->
    <div
      class="pointer-events-none fixed bottom-0 left-[var(--rail-w,0px)] right-0 z-50 flex p-3 md:p-4"
      :style="{ top: 'var(--topbar-h, 0px)' }"
    >
      <div
        class="pointer-events-auto flex min-w-0 flex-1 flex-col rounded-lg border border-slate-700 bg-surface-raised shadow-2xl"
      >
        <header
          class="flex items-center justify-between gap-3 border-b border-slate-700 px-4 py-2"
        >
          <h3 class="min-w-0 truncate text-sm font-semibold text-slate-200">
            {{ t("goto.sky.title") }}
          </h3>
          <button
            class="rounded p-1 text-slate-300 hover:bg-slate-700"
            :aria-label="t('goto.sky.close')"
            :title="t('goto.sky.close')"
            @click="emit('close')"
          >
            <IconX />
          </button>
        </header>
        <div class="min-h-0 flex-1 p-3 md:p-4">
          <slot />
        </div>
      </div>
    </div>
  </Teleport>
</template>
