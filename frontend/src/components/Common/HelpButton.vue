<script setup lang="ts">
// The only way into a page tour. Deliberately discreet and deliberately manual: the tour never
// starts on its own, so it is there when someone wants it and invisible when they do not.
//
// Drop it in a page header beside the <h1>. It renders NOTHING for a page with no tour, so adding it
// to a view is safe before that view's steps exist.
import { computed, ref } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { hasTour } from "@/constants/tour";
import IconHelp from "@/components/Icons/IconHelp.vue";
import TourModal from "@/components/Common/TourModal.vue";

const props = defineProps<{
  // Route name of the page to explain. Defaults to the current route, which is right for every
  // ordinary page; pass it explicitly for a sub-view that should borrow another page's tour.
  page?: string;
}>();

const route = useRoute();
const { t } = useI18n();
const open = ref(false);
const page = computed(() => props.page ?? String(route.name ?? ""));
const available = computed(() => hasTour(page.value));
</script>

<template>
  <button
    v-if="available"
    type="button"
    class="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-slate-500 transition-colors hover:bg-slate-200 hover:text-slate-800 motion-safe:duration-150 dark:text-slate-400 dark:hover:bg-slate-700 dark:hover:text-slate-100"
    :aria-label="t('tour.open')"
    :title="t('tour.open')"
    data-demo="page-help"
    @click="open = true"
  >
    <IconHelp />
    <span class="hidden sm:inline">{{ t("tour.help") }}</span>
  </button>
  <TourModal v-if="open" :page="page" @close="open = false" />
</template>
