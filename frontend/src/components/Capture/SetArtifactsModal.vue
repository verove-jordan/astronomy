<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watchEffect } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost, btnPrimary, checkbox } from "@/constants/styles";
import { humanizeMs } from "@/utils/format";
import FilterChip from "@/components/Common/FilterChip.vue";
import FilePreviewButton from "@/components/Common/FilePreviewButton.vue";
import IconX from "@/components/Icons/IconX.vue";
import type { SetQaReason, SetQaReport, SetQaSet } from "@/types";

// Results of the pre-stack stray-light check: every light set with its verdict, a checkbox per set
// (checked = exclude from the stack), and the aggregate impact of the current choice. "Apply"
// hands the excluded ids back to the run form; "Keep all" clears any exclusion.
const props = defineProps<{
  report: SetQaReport;
  initial: string[];
}>();
const emit = defineEmits<{
  (e: "apply", ids: string[]): void;
  (e: "close"): void;
}>();
const { t } = useI18n();

// Re-opening the modal keeps the user's previous choice; a fresh analysis pre-checks the flagged sets.
const excluded = ref<Set<string>>(
  new Set(
    props.initial.length
      ? props.initial
      : props.report.sets.filter((s) => s.flagged).map((s) => s.id),
  ),
);

const rows = computed<SetQaSet[]>(() =>
  [...props.report.sets].sort((a, b) =>
    a.flagged !== b.flagged ? (a.flagged ? -1 : 1) : b.score - a.score,
  ),
);

function toggle(id: string, on: boolean) {
  const next = new Set(excluded.value);
  if (on) next.add(id);
  else next.delete(id);
  excluded.value = next;
}

const allIds = computed(() => props.report.sets.map((s) => s.id));
const allOn = computed(
  () =>
    allIds.value.length > 0 &&
    allIds.value.every((id) => excluded.value.has(id)),
);
const noneOn = computed(() => excluded.value.size === 0);
function toggleAll(on: boolean) {
  excluded.value = on ? new Set(allIds.value) : new Set();
}
// Vue can't bind the tri-state `indeterminate` property declaratively — drive it from a ref.
const allBox = ref<HTMLInputElement | null>(null);
watchEffect(() => {
  if (allBox.value) allBox.value.indeterminate = !allOn.value && !noneOn.value;
});

// Aggregate impact of the CURRENT selection, per filter: lost share + predicted SNR multiplier.
interface FilterImpact {
  filter: string;
  pct: number;
  snr: number;
  empties: boolean;
}
const impacts = computed<FilterImpact[]>(() => {
  const byFilter = new Map<string, { lost: number; total: number }>();
  for (const s of props.report.sets) {
    const cur = byFilter.get(s.impact.filter) ?? {
      lost: 0,
      total: s.impact.filter_integration_ms,
    };
    if (excluded.value.has(s.id)) cur.lost += s.total_integration_ms;
    byFilter.set(s.impact.filter, cur);
  }
  return [...byFilter.entries()]
    .filter(([, v]) => v.lost > 0 && v.total > 0)
    .map(([filter, v]) => {
      const kept = Math.max(v.total - v.lost, 0);
      return {
        filter: filter || "?",
        pct: (100 * v.lost) / v.total,
        snr: Math.sqrt(kept / v.total),
        empties: kept <= 0,
      };
    });
});
const emptiedFilters = computed(() =>
  impacts.value.filter((i) => i.empties).map((i) => i.filter),
);

function setLine(s: SetQaSet): string {
  const parts: string[] = [];
  if (s.key.session) parts.push(s.key.session);
  parts.push(humanizeMs(s.key.exposure_ms), `gain ${s.key.gain}`);
  parts.push(
    t("setqa.frames", { n: s.count, time: humanizeMs(s.total_integration_ms) }),
  );
  return parts.join(" · ");
}

function reasonText(r: SetQaReason): string {
  return t(`setqa.reason.${r.code}`, {
    sigma: r.sigma.toFixed(1),
    pct: r.amplitude_pct.toFixed(1),
    border: r.border ? t(`setqa.border.${r.border}`) : "",
    channel: r.channel ?? "",
  });
}

// Severity styling: HIGH score = bad (inverse of the shared quality scoreTier palette).
function severityPill(s: SetQaSet): string {
  if (!s.measured)
    return "bg-slate-200 text-slate-500 dark:bg-slate-700 dark:text-slate-400";
  if (s.score >= 50)
    return "bg-danger-100 text-danger-800 dark:bg-danger-900/40 dark:text-danger-300";
  if (s.score >= 25)
    return "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300";
  return "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300";
}

function apply() {
  emit("apply", [...excluded.value]);
}
function keepAll() {
  emit("apply", []);
}

function onEsc(e: KeyboardEvent) {
  if (e.key === "Escape") emit("close");
}
onMounted(() => window.addEventListener("keydown", onEsc));
onBeforeUnmount(() => window.removeEventListener("keydown", onEsc));
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
        class="pointer-events-auto flex max-h-full w-full max-w-3xl flex-col rounded-lg border border-slate-700 bg-surface-raised shadow-2xl"
      >
        <header
          class="flex items-center justify-between gap-3 border-b border-slate-700 px-4 py-2"
        >
          <div class="min-w-0">
            <h3 class="truncate text-sm font-semibold text-slate-200">
              {{ t("setqa.title") }}
            </h3>
            <p class="text-xs text-slate-400">
              {{
                report.flagged
                  ? t("setqa.subtitle", { n: report.flagged })
                  : t("setqa.none")
              }}
            </p>
          </div>
          <button
            class="rounded p-1 text-slate-300 hover:bg-slate-700"
            :aria-label="t('setqa.close')"
            :title="t('setqa.close')"
            @click="emit('close')"
          >
            <IconX />
          </button>
        </header>

        <div class="min-h-0 flex-1 overflow-y-auto p-3 md:p-4">
          <p
            v-for="(w, i) in report.warnings"
            :key="i"
            class="mb-1 text-xs text-warning"
          >
            ⚠ {{ w }}
          </p>

          <label
            class="mb-2 flex w-max cursor-pointer items-center gap-2 text-xs font-medium text-slate-300"
          >
            <input
              ref="allBox"
              type="checkbox"
              :class="checkbox"
              :checked="allOn"
              @change="toggleAll(($event.target as HTMLInputElement).checked)"
            />
            {{ allOn ? t("setqa.deselectAll") : t("setqa.selectAll") }}
          </label>

          <ul class="space-y-2">
            <li
              v-for="s in rows"
              :key="s.id"
              class="rounded-md border px-2 py-2"
              :class="
                s.flagged
                  ? 'border-amber-500/40 bg-amber-500/5'
                  : 'border-slate-700'
              "
            >
              <label class="flex cursor-pointer items-start gap-2">
                <input
                  type="checkbox"
                  :class="checkbox"
                  class="mt-1"
                  :checked="excluded.has(s.id)"
                  @change="
                    toggle(s.id, ($event.target as HTMLInputElement).checked)
                  "
                />
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center gap-2 text-sm">
                    <FilterChip v-if="s.key.filter" :filter="s.key.filter" />
                    <span v-else class="text-slate-400">—</span>
                    <span class="text-xs text-slate-400">{{ setLine(s) }}</span>
                    <span
                      class="ml-auto shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
                      :class="severityPill(s)"
                    >
                      {{
                        s.measured
                          ? t("setqa.score", { n: Math.round(s.score) })
                          : t("setqa.unmeasured")
                      }}
                    </span>
                  </div>
                  <p
                    v-for="(r, i) in s.reasons"
                    :key="i"
                    class="mt-0.5 text-xs text-amber-500"
                  >
                    {{ reasonText(r) }}
                  </p>
                  <p class="mt-0.5 text-xs text-slate-400">
                    {{
                      t("setqa.impact", {
                        pct: s.impact.lost_integration_pct.toFixed(0),
                        filter: s.impact.filter || "?",
                        snr: s.impact.snr_factor.toFixed(2),
                      })
                    }}
                    <span
                      v-if="s.impact.empties_filter"
                      class="ml-1 text-warning"
                    >
                      {{
                        t("setqa.emptiesFilter", {
                          filter: s.impact.filter || "?",
                        })
                      }}
                    </span>
                  </p>
                </div>
                <FilePreviewButton
                  v-if="s.preview_frame"
                  :path="s.preview_frame"
                />
              </label>
            </li>
          </ul>
        </div>

        <footer
          class="flex flex-wrap items-center gap-2 border-t border-slate-700 px-4 py-2"
        >
          <div class="min-w-0 flex-1 text-xs text-slate-400">
            <template v-if="impacts.length">
              <span v-for="i in impacts" :key="i.filter" class="mr-3">
                {{
                  t("setqa.excludedSummary", {
                    filter: i.filter,
                    pct: i.pct.toFixed(0),
                    snr: i.snr.toFixed(2),
                  })
                }}
              </span>
              <span v-if="emptiedFilters.length" class="text-warning">
                {{
                  t("setqa.emptiesFilter", {
                    filter: emptiedFilters.join(", "),
                  })
                }}
              </span>
            </template>
            <span v-else>{{ t("setqa.noExclusion") }}</span>
          </div>
          <button :class="btnGhost" @click="keepAll">
            {{ t("setqa.keepAll") }}
          </button>
          <button :class="btnPrimary" @click="apply">
            {{ t("setqa.apply") }}
          </button>
        </footer>
      </div>
    </div>
  </Teleport>
</template>
