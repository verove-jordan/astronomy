<script setup lang="ts">
// The "Stacking & rejection" panel of the run form's Advanced parameters: pick how the frames are
// combined, which outlier test rejects pixels (and with what parameters), how the frames are
// normalized and weighted. Every option carries a plain-language explanation of what it does and
// what to expect from it, and the algorithm the engine would pick on its own is badged
// "recommended" for the capture in hand.
//
// The panel is a TYPED EDITOR OVER THE SAME `params` JSON the Advanced box already owns: it reads
// the current values and emits a patch the parent merges, so presets, the run's param chips, the
// per-stage rerun and run.json provenance all keep working with no extra plumbing, and the JSON
// textarea and this panel stay two views of one value.
//
// The menu itself comes from the ENGINE (GET /api/mode-params → stack_menu, backed by
// internal/stackalg): no algorithm list is hardcoded here, so the dropdown can never offer
// something the engine cannot run.
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { input as inputCls, checkbox as checkboxCls } from "@/constants/styles";
import type { KnobRange, StackMenu, StackRejectInfo } from "@/stores/jobs";

const props = defineProps<{
  menu?: StackMenu | null;
  params?: Record<string, unknown> | null;
  defaults?: Record<string, unknown> | null;
  ranges?: Record<string, KnobRange> | null;
  // frameCount, when known from the inspected capture, drives the "recommended for N frames" badge
  // and the live explanation of what "automatic" resolves to.
  frameCount?: number;
  // frameCounts maps each calibration frame type's wire prefix ("master_dark") to how many frames
  // the inspected capture holds. A type with none is not offered, and each present type resolves its
  // own count-adaptive recommendation.
  frameCounts?: Record<string, number> | null;
  // comet mode additionally exposes the comet-aligned stack's own rejection.
  showComet?: boolean;
  disabled?: boolean;
}>();
const emit = defineEmits<{
  (e: "patch", patch: Record<string, unknown>): void;
}>();
const { t, te } = useI18n();

// value reads a knob from the live JSON, falling back to the mode's default — so the controls show
// the run's real value whether or not the key is present in the box.
function value<T>(key: string, fallback: T): T {
  const v = props.params?.[key] ?? props.defaults?.[key];
  return (v === undefined || v === null ? fallback : v) as T;
}

function set(key: string, v: unknown): void {
  if (props.disabled) return;
  emit("patch", { [key]: v });
}

function setNumber(key: string, raw: string): void {
  const n = Number(raw);
  if (raw === "" || Number.isNaN(n)) return;
  set(key, n);
}

const engine = computed(() => String(value("stack_engine", "auto")));
const combine = computed(() => String(value("stack_combine", "mean")));
const reject = computed(() => String(value("stack_reject", "auto")));
const norm = computed(() => String(value("stack_norm", "addscale")));
const weight = computed(() => String(value("stack_weight", "none")));

// resolvedReject is what "automatic" becomes for this capture — the same count-adaptive rule the
// engine applies, read from the menu's auto_bands rather than re-implemented here.
const resolvedReject = computed(() => {
  const n = props.frameCount ?? 0;
  if (!props.menu || n <= 0) return "";
  for (const b of props.menu.auto_bands) {
    const aboveFrom = !b.from || n >= b.from;
    const belowTo = !b.up_to || n <= b.up_to;
    if (aboveFrom && belowTo) return b.reject;
  }
  return "";
});

// activeReject is the rejection actually in force (the explicit choice, or what auto resolves to),
// which is what the parameter controls and the explanation must describe.
const activeReject = computed<StackRejectInfo | undefined>(() => {
  const id = reject.value === "auto" ? resolvedReject.value : reject.value;
  return props.menu?.rejects.find((r) => r.id === id);
});

// The combination method decides whether a rejection/normalization/weighting is meaningful at all:
// Siril takes none of them on sum/min/max.
const combineInfo = computed(() =>
  props.menu?.combines.find((c) => c.id === combine.value),
);
// A method the catalogue does not describe falls back to mean-like behaviour; a described one is
// taken at its word, so `sum` (which Siril accepts none of these on) really does disable them.
const acceptsRejection = computed(() => combineInfo.value?.rejects ?? true);
const acceptsNorm = computed(() => combineInfo.value?.normalizes ?? true);

// showParams: an explicit "no rejection" (or a method that takes none) has no parameters to show.
const showParams = computed(
  () => acceptsRejection.value && !!activeReject.value?.has_params,
);

// paramLabel names each rejection parameter in its OWN units — "σ low" means nothing to percentile
// clipping, where the same box is a kept fraction.
function paramLabel(side: "low" | "high"): string {
  const kind = activeReject.value?.[side]?.kind ?? "sigma";
  return t(`stacking.param.${kind}.${side}`);
}

// paramHint shows the algorithm's own usable range, which differs per algorithm even though the
// knob's stored clamp is a shared union.
function paramHint(side: "low" | "high"): string {
  const p = activeReject.value?.[side];
  if (!p) return "";
  return `${p.min} – ${p.max}`;
}

// currentParam shows the value in force: the user's own number, or the algorithm's default when the
// knob is left at 0 ("use the algorithm's default").
function currentParam(side: "low" | "high"): number | "" {
  const stored = Number(value(`stack_reject_${side}`, 0));
  if (stored > 0) return stored;
  const d = activeReject.value?.[side]?.default;
  return d === undefined ? "" : d;
}

// i18n lookups fall back to the raw id, so an algorithm added to the Go catalogue still renders
// (untranslated) instead of showing an empty option.
function algoLabel(family: string, id: string): string {
  const key = `stackAlgo.${family}.${id}.label`;
  return te(key) ? t(key) : id;
}
function algoText(
  family: string,
  id: string,
  field: "desc" | "expect",
): string {
  const key = `stackAlgo.${family}.${id}.${field}`;
  return te(key) ? t(key) : "";
}

// engineOf names which engine will run a given algorithm, so a Go-only choice is visible up front
// (it is slower and has no Siril equivalent).
function isNativeOnly(engines: string[]): boolean {
  return !engines.includes("siril");
}

// The trimmed mean's own parameter — the fraction dropped at EACH end. Only that method has it, so
// it appears beside the method rather than in the rejection block.
const showTrim = computed(() => combine.value === "trimmed_mean");

// Local normalization has no Siril equivalent, so switching it on moves the run to the Go combiner.
const localNorm = computed(() => !!value("stack_local_norm", false));

const rejectOptions = computed(() => props.menu?.rejects ?? []);
const combineOptions = computed(() => props.menu?.combines ?? []);

// cometReject mirrors the main control for the comet-aligned half of a comet run.
const cometReject = computed(() => String(value("comet_stack_reject", "auto")));

// --- Calibration masters: one recipe per frame type ---
//
// Only the types actually PRESENT in the inspected capture are offered — a panel row for flats you
// did not shoot is noise. Each row resolves its own "recommended" from its OWN pool depth, which is
// the point: 200 bias frames and 5 flats want opposite algorithms.
const masterTypes = computed(() =>
  (props.menu?.master_types ?? []).filter(
    (k) => (props.frameCounts?.[k] ?? 0) > 0,
  ),
);

function masterValue(prefix: string, field: string, fallback: string | number) {
  return value(`${prefix}_${field}`, fallback);
}

// recommendedFor is what "automatic" resolves to for a pool of this depth — the same count-adaptive
// rule the engine applies, read from the menu rather than reimplemented.
function recommendedFor(count: number): string {
  if (!props.menu || count <= 0) return "";
  for (const b of props.menu.auto_bands) {
    if ((!b.from || count >= b.from) && (!b.up_to || count <= b.up_to))
      return b.reject;
  }
  return "";
}

// masterParams returns the two parameter fields for a frame type's CURRENT algorithm, so they are
// labelled in that algorithm's own units — as for the light stack.
function masterAlgo(prefix: string): StackRejectInfo | undefined {
  const id = String(masterValue(prefix, "reject", "auto"));
  const resolved =
    id === "auto" ? recommendedFor(props.frameCounts?.[prefix] ?? 0) : id;
  return props.menu?.rejects.find((r) => r.id === resolved);
}

function masterParamLabel(prefix: string, side: "low" | "high"): string {
  const kind = masterAlgo(prefix)?.[side]?.kind ?? "sigma";
  return t(`stacking.param.${kind}.${side}`);
}

function masterParamValue(prefix: string, side: "low" | "high"): number | "" {
  const stored = Number(masterValue(prefix, side, 0));
  if (stored > 0) return stored;
  const d = masterAlgo(prefix)?.[side]?.default;
  return d === undefined ? "" : d;
}

function masterHasParams(prefix: string): boolean {
  return !!masterAlgo(prefix)?.has_params;
}
</script>

<template>
  <section
    v-if="menu"
    class="mt-4 border-t border-slate-200 pt-3 dark:border-slate-700"
  >
    <h4 class="text-xs font-semibold uppercase tracking-wide text-slate-500">
      {{ t("stacking.section") }}
    </h4>
    <p class="mt-0.5 text-xs text-slate-400">{{ t("stacking.sectionHint") }}</p>

    <div class="mt-3 grid gap-3 sm:grid-cols-2">
      <!-- Combination: how the surviving samples of a pixel become one value. -->
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">{{
          t("stacking.combine")
        }}</span>
        <select
          :class="inputCls"
          :value="combine"
          :disabled="disabled"
          data-demo="stack-combine"
          @change="
            set('stack_combine', ($event.target as HTMLSelectElement).value)
          "
        >
          <option v-for="c in combineOptions" :key="c.id" :value="c.id">
            {{ algoLabel("combine", c.id)
            }}{{
              isNativeOnly(c.engines) ? " " + t("stacking.nativeSuffix") : ""
            }}
          </option>
        </select>
      </label>

      <!-- Rejection: which samples are thrown away before the average. -->
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">{{
          t("stacking.reject")
        }}</span>
        <select
          :class="inputCls"
          :value="reject"
          :disabled="disabled || !acceptsRejection"
          data-demo="stack-reject"
          @change="
            set('stack_reject', ($event.target as HTMLSelectElement).value)
          "
        >
          <option value="auto">
            {{ t("stacking.auto")
            }}{{
              resolvedReject ? " — " + algoLabel("reject", resolvedReject) : ""
            }}
          </option>
          <option v-for="r in rejectOptions" :key="r.id" :value="r.id">
            {{ algoLabel("reject", r.id)
            }}{{
              isNativeOnly(r.engines) ? " " + t("stacking.nativeSuffix") : ""
            }}{{
              frameCount && r.id === resolvedReject
                ? " " + t("stacking.recommended")
                : ""
            }}
          </option>
        </select>
      </label>
    </div>

    <!-- What the current choice does, and what to expect from it. -->
    <p
      v-if="acceptsRejection && activeReject"
      class="mt-2 rounded-md bg-slate-100 px-3 py-2 text-xs leading-relaxed text-slate-600 dark:bg-slate-800/60 dark:text-slate-300"
    >
      <span class="font-semibold">{{
        algoLabel("reject", activeReject.id)
      }}</span>
      — {{ algoText("reject", activeReject.id, "desc") }}
      <span class="mt-1 block text-slate-500 dark:text-slate-400">
        {{ algoText("reject", activeReject.id, "expect") }}
      </span>
      <span
        v-if="reject === 'auto' && frameCount"
        class="mt-1 block text-slate-500 dark:text-slate-400"
      >
        {{ t("stacking.autoChose", { n: frameCount }) }}
      </span>
    </p>
    <p
      v-else-if="!acceptsRejection"
      class="mt-2 rounded-md bg-slate-100 px-3 py-2 text-xs leading-relaxed text-slate-600 dark:bg-slate-800/60 dark:text-slate-300"
    >
      {{ algoText("combine", combine, "desc") }}
      <span class="mt-1 block text-slate-500 dark:text-slate-400">
        {{ t("stacking.noRejection") }}
      </span>
    </p>

    <!-- The chosen algorithm's own two parameters, labelled in ITS units. -->
    <div v-if="showParams" class="mt-3 grid gap-3 sm:grid-cols-2">
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">
          {{ paramLabel("low") }}
          <span class="font-normal text-slate-400"
            >({{ paramHint("low") }})</span
          >
        </span>
        <input
          type="number"
          step="0.05"
          :class="inputCls"
          :value="currentParam('low')"
          :disabled="disabled"
          data-demo="stack-reject-low"
          @change="
            setNumber(
              'stack_reject_low',
              ($event.target as HTMLInputElement).value,
            )
          "
        />
      </label>
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">
          {{ paramLabel("high") }}
          <span class="font-normal text-slate-400"
            >({{ paramHint("high") }})</span
          >
        </span>
        <input
          type="number"
          step="0.05"
          :class="inputCls"
          :value="currentParam('high')"
          :disabled="disabled"
          data-demo="stack-reject-high"
          @change="
            setNumber(
              'stack_reject_high',
              ($event.target as HTMLInputElement).value,
            )
          "
        />
      </label>
    </div>

    <!-- The trimmed mean's own parameter: how much is discarded at each end. -->
    <label v-if="showTrim" class="mt-3 block text-sm sm:w-1/2 sm:pr-1.5">
      <span class="mb-1 block text-xs font-medium text-slate-500">
        {{ t("stacking.trimFrac") }}
        <span class="font-normal text-slate-400">(0 – 0.45)</span>
      </span>
      <input
        type="number"
        step="0.05"
        min="0"
        max="0.45"
        :class="inputCls"
        :value="value('stack_trim_frac', 0)"
        :disabled="disabled"
        data-demo="stack-trim-frac"
        @change="
          setNumber(
            'stack_trim_frac',
            ($event.target as HTMLInputElement).value,
          )
        "
      />
      <span class="mt-1 block text-xs text-slate-400">{{
        t("stacking.trimFracHint")
      }}</span>
    </label>

    <!-- Normalization + weighting: how frames are brought onto one footing and how much each counts. -->
    <div class="mt-3 grid gap-3 sm:grid-cols-2">
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">{{
          t("stacking.norm")
        }}</span>
        <select
          :class="inputCls"
          :value="norm"
          :disabled="disabled || !acceptsNorm"
          data-demo="stack-norm"
          @change="
            set('stack_norm', ($event.target as HTMLSelectElement).value)
          "
        >
          <option v-for="n in menu.norms" :key="n" :value="n">
            {{ algoLabel("norm", n) }}
          </option>
        </select>
        <span class="mt-1 block text-xs text-slate-400">{{
          algoText("norm", norm, "desc")
        }}</span>
      </label>
      <label class="block text-sm">
        <span class="mb-1 block text-xs font-medium text-slate-500">{{
          t("stacking.weight")
        }}</span>
        <select
          :class="inputCls"
          :value="weight"
          :disabled="disabled || !acceptsRejection"
          data-demo="stack-weight"
          @change="
            set('stack_weight', ($event.target as HTMLSelectElement).value)
          "
        >
          <option v-for="w in menu.weights" :key="w" :value="w">
            {{ algoLabel("weight", w) }}
          </option>
        </select>
        <span class="mt-1 block text-xs text-slate-400">{{
          algoText("weight", weight, "desc")
        }}</span>
      </label>
    </div>

    <!-- The comet-aligned half of a comet run keeps its own, deliberately asymmetric rejection. -->
    <label v-if="showComet" class="mt-3 block text-sm">
      <span class="mb-1 block text-xs font-medium text-slate-500">{{
        t("stacking.cometReject")
      }}</span>
      <select
        :class="inputCls"
        :value="cometReject"
        :disabled="disabled"
        data-demo="stack-comet-reject"
        @change="
          set('comet_stack_reject', ($event.target as HTMLSelectElement).value)
        "
      >
        <option value="auto">{{ t("stacking.auto") }}</option>
        <option v-for="r in rejectOptions" :key="r.id" :value="r.id">
          {{ algoLabel("reject", r.id) }}
        </option>
      </select>
      <span class="mt-1 block text-xs text-slate-400">{{
        t("stacking.cometRejectHint")
      }}</span>
    </label>

    <!-- Extras: cheap estimators, a diagnostic rejection map, edge feathering. -->
    <div class="mt-3 space-y-1.5">
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          :class="checkboxCls"
          :checked="!!value('stack_fast_norm', false)"
          :disabled="disabled || !acceptsNorm"
          data-demo="stack-fast-norm"
          @change="
            set('stack_fast_norm', ($event.target as HTMLInputElement).checked)
          "
        />
        <span>{{ t("stacking.fastNorm") }}</span>
        <span class="text-xs text-slate-400">{{
          t("stacking.fastNormHint")
        }}</span>
      </label>
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          :class="checkboxCls"
          :checked="!!value('stack_rejection_maps', false)"
          :disabled="disabled || !acceptsRejection"
          data-demo="stack-rejection-maps"
          @change="
            set(
              'stack_rejection_maps',
              ($event.target as HTMLInputElement).checked,
            )
          "
        />
        <span>{{ t("stacking.rejectionMaps") }}</span>
        <span class="text-xs text-slate-400">{{
          t("stacking.rejectionMapsHint")
        }}</span>
      </label>
    </div>

    <!-- Local normalization: a per-frame background/transparency surface, Go engine only. -->
    <div class="mt-3">
      <label class="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          :class="checkboxCls"
          :checked="localNorm"
          :disabled="disabled || !acceptsRejection"
          data-demo="stack-local-norm"
          @change="
            set('stack_local_norm', ($event.target as HTMLInputElement).checked)
          "
        />
        <span>{{ t("stacking.localNorm") }}</span>
        <span class="text-xs text-slate-400">{{
          t("stacking.localNormHint")
        }}</span>
      </label>
      <label v-if="localNorm" class="mt-2 block text-sm sm:w-1/2 sm:pr-1.5">
        <span class="mb-1 block text-xs font-medium text-slate-500">
          {{ t("stacking.localNormDegree") }}
          <span class="font-normal text-slate-400">(1 – 4)</span>
        </span>
        <input
          type="number"
          step="1"
          min="1"
          max="4"
          :class="inputCls"
          :value="value('stack_local_norm_degree', 0) || 1"
          :disabled="disabled"
          data-demo="stack-local-norm-degree"
          @change="
            setNumber(
              'stack_local_norm_degree',
              ($event.target as HTMLInputElement).value,
            )
          "
        />
      </label>
    </div>

    <!-- Edge feathering: hides the hard dithered borders of a drifting sequence. -->
    <label class="mt-3 block text-sm sm:w-1/2 sm:pr-1.5">
      <span class="mb-1 block text-xs font-medium text-slate-500">
        {{ t("stacking.feather") }}
        <span class="font-normal text-slate-400">(0 – 512 px)</span>
      </span>
      <input
        type="number"
        step="5"
        min="0"
        max="512"
        :class="inputCls"
        :value="value('stack_feather', 0)"
        :disabled="disabled || !acceptsRejection"
        data-demo="stack-feather"
        @change="
          setNumber('stack_feather', ($event.target as HTMLInputElement).value)
        "
      />
      <span class="mt-1 block text-xs text-slate-400">{{
        t("stacking.featherHint")
      }}</span>
    </label>

    <!-- Calibration masters: one recipe per frame type, since each is stacked separately and their
         pools differ by an order of magnitude. Only the types the capture actually contains appear. -->
    <details v-if="masterTypes.length" class="mt-4">
      <summary class="cursor-pointer text-xs font-medium text-slate-500">
        {{ t("stacking.masters") }}
      </summary>
      <p class="mt-1 text-xs text-slate-400">{{ t("stacking.mastersHint") }}</p>
      <div
        v-for="k in masterTypes"
        :key="k"
        class="mt-3 rounded-md border border-slate-200 p-2.5 dark:border-slate-700"
      >
        <h5
          class="flex flex-wrap items-baseline gap-x-2 text-xs font-semibold text-slate-600 dark:text-slate-300"
        >
          {{ t(`stacking.frameType.${k}`) }}
          <span class="font-normal text-slate-400">
            {{ t("stacking.frameCount", { n: frameCounts?.[k] ?? 0 }) }}
          </span>
        </h5>
        <div class="mt-2 grid gap-3 sm:grid-cols-2">
          <label class="block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("stacking.combine")
            }}</span>
            <select
              :class="inputCls"
              :value="masterValue(k, 'combine', 'mean')"
              :disabled="disabled"
              :data-demo="`${k}-combine`"
              @change="
                set(`${k}_combine`, ($event.target as HTMLSelectElement).value)
              "
            >
              <option v-for="c in combineOptions" :key="c.id" :value="c.id">
                {{ algoLabel("combine", c.id) }}
              </option>
            </select>
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              t("stacking.reject")
            }}</span>
            <select
              :class="inputCls"
              :value="masterValue(k, 'reject', 'auto')"
              :disabled="disabled"
              :data-demo="`${k}-reject`"
              @change="
                set(`${k}_reject`, ($event.target as HTMLSelectElement).value)
              "
            >
              <option value="auto">
                {{ t("stacking.auto")
                }}{{
                  recommendedFor(frameCounts?.[k] ?? 0)
                    ? " — " +
                      algoLabel("reject", recommendedFor(frameCounts?.[k] ?? 0))
                    : ""
                }}
              </option>
              <option v-for="r in rejectOptions" :key="r.id" :value="r.id">
                {{ algoLabel("reject", r.id)
                }}{{
                  isNativeOnly(r.engines)
                    ? " " + t("stacking.nativeSuffix")
                    : ""
                }}{{
                  (frameCounts?.[k] ?? 0) &&
                  r.id === recommendedFor(frameCounts?.[k] ?? 0)
                    ? " " + t("stacking.recommended")
                    : ""
                }}
              </option>
            </select>
          </label>
        </div>
        <div v-if="masterHasParams(k)" class="mt-2 grid gap-3 sm:grid-cols-2">
          <label class="block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              masterParamLabel(k, "low")
            }}</span>
            <input
              type="number"
              step="0.05"
              :class="inputCls"
              :value="masterParamValue(k, 'low')"
              :disabled="disabled"
              :data-demo="`${k}-low`"
              @change="
                setNumber(`${k}_low`, ($event.target as HTMLInputElement).value)
              "
            />
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-xs font-medium text-slate-500">{{
              masterParamLabel(k, "high")
            }}</span>
            <input
              type="number"
              step="0.05"
              :class="inputCls"
              :value="masterParamValue(k, 'high')"
              :disabled="disabled"
              :data-demo="`${k}-high`"
              @change="
                setNumber(
                  `${k}_high`,
                  ($event.target as HTMLInputElement).value,
                )
              "
            />
          </label>
        </div>
        <p class="mt-1.5 text-xs text-slate-400">
          {{ t(`stacking.frameTypeHint.${k}`) }}
        </p>
      </div>
      <p class="mt-2 text-xs text-slate-400">
        {{ t("stacking.mastersVariantNote") }}
      </p>
    </details>

    <p
      v-if="engine !== 'auto'"
      class="mt-2 text-xs text-warning-600 dark:text-warning-300"
    >
      {{ t("stacking.engineOverride", { engine }) }}
    </p>
  </section>
</template>
