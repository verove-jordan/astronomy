<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import IconWarning from "@/components/Icons/IconWarning.vue";
import IconX from "@/components/Icons/IconX.vue";
import { input, td } from "@/constants/styles";
import type { SkyEyepiece } from "@/types";
import { eyepieceView, exitPupilOutOfRange } from "@/utils/optics";

// The visual kit, as a table you can read. The three editable numbers (focal, apparent field, label)
// sit beside what they actually GIVE on this scope — magnification, true field, exit pupil — recomputed
// locally on every keystroke, so a reducer or Barlow change is visible before the debounced refetch.
//
// Column widths come from <colgroup>, NOT from width utilities stacked on the shared `input` token:
// `input` starts with w-full and Tailwind emits w-full after w-16, so `[input, 'w-16']` silently loses
// and squeezes every other cell. That is what made the old row stack unreadable.

const props = defineProps<{
  modelValue: SkyEyepiece[];
  effectiveFocalMm: number;
  apertureMm: number;
}>();

const emit = defineEmits<{ "update:modelValue": [SkyEyepiece[]] }>();

const { t } = useI18n();

// Each row carries its own derived view so the template stays declarative and every cell reads the
// same computation. Rows are keyed by index because an eyepiece has no id — the kit is a short,
// hand-edited list.
const rows = computed(() =>
  props.modelValue.map((ep) => ({
    ep,
    view: eyepieceView(props.effectiveFocalMm, props.apertureMm, ep),
  })),
);

const anyOutOfRange = computed(() =>
  rows.value.some((r) => exitPupilOutOfRange(r.view)),
);

// Every mutation emits a NEW array of NEW rows: the parent owns the kit, so we never write through the
// prop. The parent re-commits to the store, which persists and (in visual mode) re-scores.
function patch(i: number, field: keyof SkyEyepiece, value: string | number) {
  emit(
    "update:modelValue",
    props.modelValue.map((ep, j) =>
      j === i ? { ...ep, [field]: value } : { ...ep },
    ),
  );
}

function addEyepiece() {
  emit("update:modelValue", [
    ...props.modelValue.map((ep) => ({ ...ep })),
    { label: "", focal_mm: 0, afov_deg: 60 },
  ]);
}

function removeEyepiece(i: number) {
  emit(
    "update:modelValue",
    props.modelValue.filter((_, j) => j !== i).map((ep) => ({ ...ep })),
  );
}

// numeric() keeps an in-progress edit from collapsing to NaN: a cleared field reads as 0, which the
// kit filter already treats as "incomplete row, don't send".
function numeric(value: string): number {
  const n = parseFloat(value);
  return Number.isFinite(n) ? n : 0;
}

const fmtMag = (v: number) => (v > 0 ? `${Math.round(v)}×` : "—");
const fmtDeg = (v: number) => (v > 0 ? `${v.toFixed(2)}°` : "—");
const fmtMm = (v: number) => (v > 0 ? `${v.toFixed(1)} mm` : "—");

// Header cell: the shared `th` token is built for sortable headers (it carries cursor-pointer and a
// hover colour). These headers are labels, so they use the same typography without the affordance.
const hdr =
  "whitespace-nowrap px-2 py-1 text-left text-[10px] font-semibold uppercase tracking-wide text-slate-500 dark:text-slate-400";
const cell = `${td} !px-1 !py-1`;
const derived =
  "hidden whitespace-nowrap px-2 py-1 text-right text-xs tabular-nums text-slate-600 sm:table-cell dark:text-slate-300";
</script>

<template>
  <div class="mt-2">
    <div class="mb-1 flex flex-wrap items-baseline justify-between gap-2">
      <span class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("tonight.eyepieces.title") }}
        <span
          v-if="effectiveFocalMm > 0"
          class="ml-1 text-[10px] text-slate-400"
          >{{
            t("tonight.eyepieces.effective", {
              eff: Math.round(effectiveFocalMm),
            })
          }}</span
        >
      </span>
      <button
        type="button"
        class="text-xs text-brand-600 hover:underline dark:text-brand-300"
        @click="addEyepiece"
      >
        + {{ t("tonight.eyepieces.add") }}
      </button>
    </div>

    <div class="overflow-x-auto">
      <table class="w-full min-w-[22rem] border-collapse">
        <colgroup>
          <col class="w-[6.5rem]" />
          <col class="w-[6.5rem]" />
          <col />
          <col class="w-[4.5rem]" />
          <col class="w-[5rem]" />
          <col class="w-[5.5rem]" />
          <col class="w-8" />
        </colgroup>
        <thead>
          <tr class="border-b border-slate-200 dark:border-slate-700">
            <th scope="col" :class="hdr">{{ t("tonight.eyepieces.focal") }}</th>
            <th scope="col" :class="hdr">{{ t("tonight.eyepieces.afov") }}</th>
            <th scope="col" :class="hdr">{{ t("tonight.eyepieces.label") }}</th>
            <th scope="col" :class="[hdr, 'hidden text-right sm:table-cell']">
              {{ t("tonight.eyepieces.magnification") }}
            </th>
            <th scope="col" :class="[hdr, 'hidden text-right sm:table-cell']">
              {{ t("tonight.eyepieces.trueField") }}
            </th>
            <th scope="col" :class="[hdr, 'hidden text-right sm:table-cell']">
              {{ t("tonight.eyepieces.exitPupil") }}
            </th>
            <th scope="col" :class="hdr">
              <span class="sr-only">{{ t("common.remove") }}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length">
            <td colspan="7" class="px-2 py-2 text-xs text-slate-400">
              {{ t("tonight.eyepieces.empty") }}
            </td>
          </tr>
          <tr v-for="(row, i) in rows" :key="i">
            <td :class="cell">
              <input
                :value="row.ep.focal_mm || ''"
                :class="input"
                class="!px-2 !py-1 !text-sm"
                type="number"
                step="1"
                min="1"
                :aria-label="t('tonight.eyepieces.focal')"
                @input="
                  patch(
                    i,
                    'focal_mm',
                    numeric(($event.target as HTMLInputElement).value),
                  )
                "
              />
            </td>
            <td :class="cell">
              <input
                :value="row.ep.afov_deg || ''"
                :class="input"
                class="!px-2 !py-1 !text-sm"
                type="number"
                step="1"
                min="1"
                :aria-label="t('tonight.eyepieces.afov')"
                @input="
                  patch(
                    i,
                    'afov_deg',
                    numeric(($event.target as HTMLInputElement).value),
                  )
                "
              />
            </td>
            <td :class="cell">
              <input
                :value="row.ep.label"
                :class="input"
                class="!px-2 !py-1 !text-sm"
                type="text"
                :placeholder="
                  row.ep.focal_mm > 0 ? `${row.ep.focal_mm}mm` : undefined
                "
                :aria-label="t('tonight.eyepieces.label')"
                @input="
                  patch(i, 'label', ($event.target as HTMLInputElement).value)
                "
              />
            </td>
            <td :class="derived">{{ fmtMag(row.view.magX) }}</td>
            <td :class="derived">{{ fmtDeg(row.view.trueFovDeg) }}</td>
            <td
              :class="[
                derived,
                exitPupilOutOfRange(row.view)
                  ? 'text-amber-600 dark:text-amber-400'
                  : '',
              ]"
              :title="
                exitPupilOutOfRange(row.view)
                  ? t('tonight.visual.exitWarn')
                  : undefined
              "
            >
              {{ fmtMm(row.view.exitPupilMm) }}
            </td>
            <td class="px-1 py-1 text-right">
              <button
                type="button"
                class="text-slate-400 hover:text-danger-500"
                :aria-label="t('tonight.eyepieces.remove')"
                :title="t('tonight.eyepieces.remove')"
                @click="removeEyepiece(i)"
              >
                <IconX class="h-3.5 w-3.5" />
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <p
      v-if="anyOutOfRange"
      class="mt-1 flex items-center gap-1 text-[10px] text-amber-600 dark:text-amber-400"
      role="status"
    >
      <IconWarning class="h-3 w-3 shrink-0" />
      {{ t("tonight.visual.exitWarn") }}
    </p>
  </div>
</template>
