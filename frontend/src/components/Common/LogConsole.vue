<script setup lang="ts">
import {
  computed,
  nextTick,
  onMounted,
  onBeforeUnmount,
  ref,
  watch,
} from "vue";
import { useI18n } from "vue-i18n";
import { cardElevated } from "@/constants/styles";
import { formatTimestamp } from "@/utils/format";
import type { LogLine } from "@/types";
import IconChevronDown from "@/components/Icons/IconChevronDown.vue";

const props = defineProps<{
  lines: LogLine[];
  collapsible?: boolean;
  modelValue?: boolean; // true = collapsed (when collapsible)
  title?: string;
}>();
const emit = defineEmits<{ "update:modelValue": [v: boolean] }>();
const { t } = useI18n();

const follow = ref(true);
const body = ref<HTMLElement | null>(null);
const maxHeightPx = ref(320);
let programmatic = false; // true while WE scroll, so onScroll doesn't mistake it for a manual scroll-up

const collapsed = computed(
  () => !!props.collapsible && props.modelValue === true,
);

// Only the last RENDER_CAP lines are put in the DOM — a live log's buffer runs to thousands of lines
// (5000 in useJobStream), and rendering them all is the main heaviness on the job page. The header still
// shows the full count; following/scroll math operates on the rendered tail.
const RENDER_CAP = 800;
const visible = computed(() =>
  props.lines.length > RENDER_CAP
    ? props.lines.slice(-RENDER_CAP)
    : props.lines,
);

// The console fills from its own top to near the viewport bottom and scrolls INTERNALLY, so the page
// itself never grows tall enough to scroll. BOTTOM_GAP leaves the card's + page's bottom padding plus a
// little breathing room. Recomputed on mount, on resize, and whenever the content above it reflows.
const BOTTOM_GAP = 48;
function recomputeHeight() {
  const el = body.value;
  if (!el) return;
  const top = el.getBoundingClientRect().top;
  const h = Math.max(160, Math.round(window.innerHeight - top - BOTTOM_GAP));
  if (Math.abs(h - maxHeightPx.value) > 1) maxHeightPx.value = h; // guard against RO feedback loops
  // A height change moves the bottom; re-pin to the tail so following stays glued to the newest logs.
  if (follow.value && !collapsed.value) nextTick(scrollToTail);
}

let ro: ResizeObserver | null = null;
onMounted(() => {
  nextTick(recomputeHeight);
  window.addEventListener("resize", recomputeHeight);
  // Content above the log (preview/summary loading in) shifts its top — re-measure on any page reflow.
  ro = new ResizeObserver(() => recomputeHeight());
  ro.observe(document.body);
});
onBeforeUnmount(() => {
  window.removeEventListener("resize", recomputeHeight);
  ro?.disconnect();
  ro = null;
});

// Snap to the newest line — instant, not smooth: tailing a live log should jump, and a smooth scroll
// would momentarily read "not at the bottom" mid-animation and flip Follow off.
function scrollToTail() {
  const el = body.value;
  if (!el) return;
  programmatic = true;
  el.scrollTop = el.scrollHeight;
  requestAnimationFrame(() => (programmatic = false)); // release after the resulting scroll event
}

watch(
  () => props.lines.length,
  async () => {
    if (!follow.value || collapsed.value) return;
    await nextTick();
    scrollToTail();
  },
);

// Turning Follow on (or un-collapsing while following) jumps straight to the newest logs.
watch([follow, collapsed], async ([f, c]) => {
  if (!f || c) return;
  await nextTick();
  scrollToTail();
});

// A manual scroll away from the bottom turns Follow off; scrolling back to it turns Follow on.
function onScroll() {
  if (programmatic) return; // ignore the scrolls we trigger ourselves
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
      class="min-w-0 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-slate-950/90 p-3 font-mono text-xs leading-relaxed text-slate-300"
      :style="{ maxHeight: maxHeightPx + 'px' }"
      @scroll="onScroll"
    >
      <template v-if="lines.length">
        <div v-for="(l, i) in visible" :key="l.seq ?? i">
          <span v-if="l.ts" class="mr-2 text-slate-500">{{
            formatTimestamp(l.ts)
          }}</span
          >{{ l.text }}
        </div>
      </template>
      <span v-else class="text-slate-500">{{ t("log.empty") }}</span>
    </div>
  </div>
</template>
