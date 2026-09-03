<script setup lang="ts">
// The page tour. A carousel of real screenshots on the left, the explanation on the right.
//
// It shows PICTURES of the page rather than spotlighting the live DOM, which is what makes it
// dependable: a tour that highlights real elements can only describe the page as it happens to be
// right now — nothing selected, a panel collapsed, a list still loading, a job not yet run — and the
// steps that matter most are exactly the ones whose elements are not on screen yet. The screenshots
// are generated from the running app (`just tour-shots`) with the focus highlight baked in, so they
// stay honest without the modal needing to know where anything is.
//
// It never opens by itself. HelpButton is the only way in.
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { TOUR_FALLBACK_LOCALE, tourShot, tourSteps } from "@/constants/tour";
import { btnGhost, btnPrimary } from "@/constants/styles";
import IconX from "@/components/Icons/IconX.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";

const props = defineProps<{ page: string }>();
const emit = defineEmits<{ (e: "close"): void }>();
const { t, locale } = useI18n();

const steps = computed(() => tourSteps(props.page));
const index = ref(0);

// Per-step image state. A shot that has not been generated yet (a new step, or a locale that has not
// been re-shot) must not leave a broken-image icon in the middle of the modal: the text is the
// substance and stands on its own, so the frame simply collapses to a caption.
const missing = ref(new Set<string>());
const src = computed(() => {
  const key = steps.value[index.value];
  if (!key) return "";
  const localized = tourShot(locale.value, props.page, key);
  if (!missing.value.has(localized)) return localized;
  const fallback = tourShot(TOUR_FALLBACK_LOCALE, props.page, key);
  return missing.value.has(fallback) ? "" : fallback;
});

function onImgError() {
  if (src.value) missing.value = new Set(missing.value).add(src.value);
}

const stepKey = computed(() => steps.value[index.value] ?? "");
const title = computed(() =>
  t(`tour.${props.page}.steps.${stepKey.value}.title`),
);
const body = computed(() =>
  t(`tour.${props.page}.steps.${stepKey.value}.body`),
);

function step(delta: number) {
  const n = steps.value.length;
  if (n === 0) return;
  index.value = (index.value + delta + n) % n;
}
function go(i: number) {
  index.value = i;
}

// Capture phase, matching StagePreviewTimeline: the arrows must drive the carousel even when focus
// sits on a button inside the modal, and must not leak to whatever is behind it.
function onKey(e: KeyboardEvent) {
  if (e.key === "Escape") {
    emit("close");
    return;
  }
  if (e.key === "ArrowRight") {
    e.preventDefault();
    step(1);
  } else if (e.key === "ArrowLeft") {
    e.preventDefault();
    step(-1);
  }
}
onMounted(() => window.addEventListener("keydown", onKey, true));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey, true));

// Reopening on another page restarts the tour rather than resuming someone else's position.
watch(
  () => props.page,
  () => (index.value = 0),
);

const arrowBtn =
  "absolute top-1/2 -translate-y-1/2 rounded-full bg-black/50 p-2 text-white transition hover:bg-black/70 focus:outline-none";
</script>

<template>
  <Teleport to="body">
    <div
      class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm"
      aria-hidden="true"
      @click="emit('close')"
    />
    <div
      class="pointer-events-none fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6"
    >
      <div
        class="pointer-events-auto flex max-h-full w-full max-w-5xl flex-col rounded-lg border border-slate-700 bg-surface-raised shadow-2xl"
        role="dialog"
        aria-modal="true"
        :aria-label="t('tour.title', { page: t(`tour.${page}.title`) })"
      >
        <header
          class="flex items-center justify-between gap-3 border-b border-slate-700 px-4 py-2"
        >
          <div class="min-w-0">
            <h3 class="min-w-0 truncate text-sm font-semibold text-slate-200">
              {{ t(`tour.${page}.title`) }}
            </h3>
            <p class="text-xs text-slate-400">
              {{ t("tour.stepOf", { n: index + 1, total: steps.length }) }}
            </p>
          </div>
          <button
            :class="btnGhost"
            class="!p-1"
            :aria-label="t('common.close')"
            :title="t('common.close')"
            @click="emit('close')"
          >
            <IconX />
          </button>
        </header>

        <div
          class="grid min-h-0 flex-1 gap-4 overflow-y-auto p-3 md:grid-cols-5 md:p-4"
        >
          <!-- Left: the preview carousel. -->
          <div class="relative md:col-span-3">
            <div
              class="relative flex aspect-[16/10] items-center justify-center overflow-hidden rounded-md border border-slate-700 bg-slate-950"
            >
              <img
                v-if="src"
                :key="src"
                :src="src"
                :alt="title"
                class="h-full w-full object-contain"
                @error="onImgError"
              />
              <p v-else class="px-6 text-center text-xs text-slate-500">
                {{ t("tour.noShot") }}
              </p>
              <template v-if="steps.length > 1">
                <button
                  :class="[arrowBtn, 'left-2']"
                  :aria-label="t('common.previous')"
                  :title="t('common.previous')"
                  @click="step(-1)"
                >
                  <IconChevronRight class="rotate-180" />
                </button>
                <button
                  :class="[arrowBtn, 'right-2']"
                  :aria-label="t('common.next')"
                  :title="t('common.next')"
                  @click="step(1)"
                >
                  <IconChevronRight />
                </button>
              </template>
            </div>
            <div
              v-if="steps.length > 1"
              class="mt-2 flex flex-wrap items-center justify-center gap-1.5"
            >
              <button
                v-for="(s, i) in steps"
                :key="s"
                class="h-2 w-2 rounded-full transition-colors motion-safe:duration-200"
                :class="
                  i === index
                    ? 'bg-brand-500'
                    : 'bg-slate-600 hover:bg-slate-500'
                "
                :aria-label="t(`tour.${page}.steps.${s}.title`)"
                :aria-current="i === index ? 'step' : undefined"
                @click="go(i)"
              />
            </div>
          </div>

          <!-- Right: the explanation. -->
          <div class="flex min-w-0 flex-col md:col-span-2">
            <h4 class="text-base font-semibold text-slate-100">{{ title }}</h4>
            <p
              class="mt-2 whitespace-pre-line text-sm leading-relaxed text-slate-300"
            >
              {{ body }}
            </p>
          </div>
        </div>

        <footer
          class="flex items-center justify-between gap-3 border-t border-slate-700 px-4 py-2"
        >
          <button :class="btnGhost" :disabled="index === 0" @click="step(-1)">
            {{ t("common.previous") }}
          </button>
          <button
            v-if="index < steps.length - 1"
            :class="btnPrimary"
            @click="step(1)"
          >
            {{ t("common.next") }}
          </button>
          <button v-else :class="btnPrimary" @click="emit('close')">
            {{ t("tour.done") }}
          </button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>
