<script setup lang="ts">
// A collapsible reference for the advanced-params JSON: every knob the SELECTED MODE exposes, grouped by
// its re-entry tier (finish / prep / re-stack), with a one-line description of what it does. Since the JSON
// box is editable (real JSON can't carry inline comments), this is the "commentary on every line". The
// knobs currently set in the JSON above are HIGHLIGHTED and show their live value; the rest are shown muted
// so the user can discover every knob they may add for this mode. Read-only, purely explanatory — it never
// edits the JSON.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { groupsForMode } from "@/constants/paramDocs";

const props = defineProps<{
  mode: string;
  params?: Record<string, unknown> | null;
}>();
const { t } = useI18n();

// A knob is "active" when the current (valid) JSON object carries that key.
function isActive(key: string): boolean {
  return (
    !!props.params && Object.prototype.hasOwnProperty.call(props.params, key)
  );
}

// The current value for a key, formatted for display (empty when absent / JSON invalid).
function valueOf(key: string): string {
  const v = props.params?.[key];
  if (v === undefined || v === null) return "";
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "string") return v === "" ? '""' : v;
  return String(v);
}

const groups = computed(() => groupsForMode(props.mode));
</script>

<template>
  <details
    class="mt-3 rounded-md border border-slate-200 dark:border-slate-700"
  >
    <summary
      class="cursor-pointer px-3 py-2 text-xs font-medium text-slate-500 hover:text-slate-700 dark:hover:text-slate-300"
    >
      {{ t("paramDocs.title") }}
    </summary>
    <div class="space-y-4 px-3 pb-3">
      <p class="text-xs text-slate-400">{{ t("paramDocs.activeLegend") }}</p>
      <section v-for="g in groups" :key="g.titleKey">
        <h4
          class="flex flex-wrap items-baseline gap-x-2 text-xs font-semibold text-slate-600 dark:text-slate-300"
        >
          {{ t(g.titleKey) }}
          <span class="font-normal text-slate-400">— {{ t(g.hintKey) }}</span>
        </h4>
        <dl class="mt-1 space-y-1">
          <div
            v-for="k in g.keys"
            :key="k"
            class="grid grid-cols-1 gap-x-3 rounded px-1.5 py-1 sm:grid-cols-[minmax(9rem,auto)_1fr]"
            :class="
              isActive(k)
                ? 'bg-brand-50 ring-1 ring-brand-200 dark:bg-brand-500/10 dark:ring-brand-500/30'
                : ''
            "
          >
            <dt
              class="flex items-baseline gap-2 font-mono text-xs"
              :class="
                isActive(k)
                  ? 'font-semibold text-brand-700 dark:text-brand-300'
                  : 'text-slate-500 dark:text-slate-400'
              "
            >
              <span>{{ k }}</span>
              <span
                v-if="isActive(k)"
                class="font-sans font-semibold text-brand-500 dark:text-brand-300"
                >{{ valueOf(k) || t("paramDocs.set") }}</span
              >
            </dt>
            <dd
              class="text-xs"
              :class="
                isActive(k)
                  ? 'text-slate-600 dark:text-slate-300'
                  : 'text-slate-500 dark:text-slate-400'
              "
            >
              {{ t(`paramDocs.${k}`) }}
            </dd>
          </div>
        </dl>
      </section>
    </div>
  </details>
</template>
