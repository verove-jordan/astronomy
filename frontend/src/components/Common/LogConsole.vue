<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { cardElevated } from "@/constants/styles";
import IconChevronDown from "@/components/Icons/IconChevronDown.vue";
import IconExpand from "@/components/Icons/IconExpand.vue";
import IconCollapse from "@/components/Icons/IconCollapse.vue";

const props = defineProps<{
  lines: string[];
  collapsible?: boolean;
  modelValue?: boolean; // true = collapsed (when collapsible)
  maxHeightClass?: string;
  title?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [v: boolean] }>();
const { t } = useI18n();

const follow = ref(true);
const expanded = ref(false);
const body = ref<HTMLElement | null>(null);
const reduced =
  typeof window !== "undefined" &&
  !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

const collapsed = computed(
  () => !!props.collapsible && props.modelValue === true,
);

// Expanded → grow to a viewport-bounded height (70dvh) so it never spills past the page bottom;
// the body scrolls internally. Otherwise the compact default (or a caller override).
const heightClass = computed(() =>
  expanded.value ? "max-h-[70dvh]" : props.maxHeightClass || "max-h-64",
);

function scrollToTail() {
  const el = body.value;
  if (el)
    el.scrollTo({
      top: el.scrollHeight,
      behavior: reduced ? "auto" : "smooth",
    });
}

watch(
  () => props.lines.length,
  async () => {
    if (!follow.value || collapsed.value) return;
    await nextTick();
    scrollToTail();
  },
);

// Keep the tail in view when the panel is resized.
watch(expanded, async () => {
  if (!follow.value || collapsed.value) return;
  await nextTick();
  scrollToTail();
});

function onScroll() {
  const el = body.value;
  if (el) follow.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
}
</script>

<template>
  <div :class="cardElevated">
    <div class="mb-2 flex items-center justify-between gap-2">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ title || t("job.logs") }}
        <span class="ml-1 text-xs font-normal text-slate-400">{{
          lines.length
        }}</span>
      </h3>
      <div class="flex items-center gap-3">
        <label
          class="flex items-center gap-1 text-xs text-slate-500 dark:text-slate-400"
        >
          <input v-model="follow" type="checkbox" class="accent-brand-500" />
          {{ t("job.followTail") }}
        </label>
        <button
          v-if="!collapsed"
          class="text-slate-500 hover:text-brand-600 dark:text-slate-400 dark:hover:text-brand-300"
          :aria-label="expanded ? t('job.collapseLogs') : t('job.expandLogs')"
          @click="expanded = !expanded"
        >
          <IconCollapse v-if="expanded" />
          <IconExpand v-else />
        </button>
        <button
          v-if="collapsible"
          class="text-slate-500 hover:text-brand-600 dark:text-slate-400 dark:hover:text-brand-300"
          :aria-label="collapsed ? t('job.showLogs') : t('job.hideLogs')"
          @click="emit('update:modelValue', !modelValue)"
        >
          <IconChevronDown :class="collapsed ? 'rotate-180' : ''" />
        </button>
      </div>
    </div>
    <div
      v-show="!collapsed"
      ref="body"
      role="log"
      aria-live="polite"
      :class="[
        'min-w-0 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-slate-950/90 p-3 font-mono text-xs leading-relaxed text-slate-300',
        heightClass,
      ]"
      @scroll="onScroll"
    >
      <template v-if="lines.length">
        <div v-for="(l, i) in lines" :key="i">{{ l }}</div>
      </template>
      <span v-else class="text-slate-500">{{ t("log.empty") }}</span>
    </div>
  </div>
</template>
