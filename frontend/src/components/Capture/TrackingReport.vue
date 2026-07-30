<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { btnGhost } from "@/constants/styles";
import { apiGet } from "@/services/api";

// What a night of subs says about the mount.
//
// Every figure here is measured from the user's own frames — no mount specification is trusted, and
// nothing is written back to the mount. The headline is the last line: how long an unguided sub can
// be, which is the number that actually decides how the next session is set up.
const props = defineProps<{ sessionId: number | null }>();
const { t } = useI18n();

interface Report {
  samples: number;
  span_sec: number;
  drift_ra_arcsec_per_min: number;
  drift_dec_arcsec_per_min: number;
  pe_amplitude_arcsec: number;
  pe_period_sec: number;
  pe_confidence: number;
  residual_rms_arcsec: number;
  max_unguided_sec: number;
  warnings?: string[];
}

interface Measuring {
  attempts: number;
  recorded: number;
  last_error?: string;
}

const report = ref<Report | null>(null);
const measuring = ref<Measuring | null>(null);
const message = ref("");
const loading = ref(false);

watch(() => props.sessionId, load, { immediate: true });

async function load() {
  if (!props.sessionId) {
    report.value = null;
    return;
  }
  loading.value = true;
  try {
    const res = await apiGet<{
      report: Report | null;
      message?: string;
      measuring?: Measuring;
    }>(`/api/tracking/report/${props.sessionId}`);
    report.value = res.report;
    measuring.value = res.measuring ?? null;
    message.value = res.message ?? "";
  } catch {
    report.value = null;
    measuring.value = null;
    message.value = t("capture.tracking.unavailable");
  } finally {
    loading.value = false;
  }
}

// A mount is only as good as its worst moment, so the periodic error is quoted peak-to-peak — the
// same convention manufacturers use, which makes it comparable with the AVX's published figure.
const quality = computed(() => {
  const pe = report.value?.pe_amplitude_arcsec ?? 0;
  if (!pe) return "";
  if (pe < 10) return t("capture.tracking.gradeGood");
  if (pe < 25) return t("capture.tracking.gradeTypical");
  return t("capture.tracking.gradeHigh");
});

// Confidence below a third means the fit is describing noise; showing the period then would be
// worse than showing nothing.
const periodTrustworthy = computed(
  () => (report.value?.pe_confidence ?? 0) >= 0.3,
);

const fmt = (v: number, digits = 1) => v.toFixed(digits);
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("capture.tracking.title") }}
      </h3>
      <button
        :class="btnGhost"
        class="!px-2 !py-0.5 text-xs"
        :disabled="!sessionId || loading"
        @click="load"
      >
        {{ t("common.refresh") }}
      </button>
    </div>

    <p class="text-[11px] text-slate-500 dark:text-slate-400">
      {{ t("capture.tracking.blurb") }}
    </p>

    <p v-if="!report" class="text-xs text-slate-400">
      {{ message || t("capture.tracking.none") }}
    </p>

    <div v-else class="space-y-2">
      <dl class="grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
        <dt class="text-slate-500 dark:text-slate-400">
          {{ t("capture.tracking.pe") }}
        </dt>
        <dd class="font-mono text-slate-700 dark:text-slate-200">
          ±{{ fmt(report.pe_amplitude_arcsec / 2) }}″
          <span class="text-slate-400"
            >({{ fmt(report.pe_amplitude_arcsec) }}″ p-p)</span
          >
          <span v-if="quality" class="ml-1 text-slate-400"
            >· {{ quality }}</span
          >
        </dd>

        <dt class="text-slate-500 dark:text-slate-400">
          {{ t("capture.tracking.period") }}
        </dt>
        <dd class="font-mono text-slate-700 dark:text-slate-200">
          <template v-if="periodTrustworthy"
            >{{ fmt(report.pe_period_sec, 0) }} s</template
          >
          <span v-else class="text-slate-400">{{
            t("capture.tracking.notDetected")
          }}</span>
        </dd>

        <dt class="text-slate-500 dark:text-slate-400">
          {{ t("capture.tracking.drift") }}
        </dt>
        <dd class="font-mono text-slate-700 dark:text-slate-200">
          {{ fmt(report.drift_ra_arcsec_per_min, 2) }} /
          {{ fmt(report.drift_dec_arcsec_per_min, 2) }} ″/min
        </dd>

        <dt class="text-slate-500 dark:text-slate-400">
          {{ t("capture.tracking.residual") }}
        </dt>
        <dd class="font-mono text-slate-700 dark:text-slate-200">
          {{ fmt(report.residual_rms_arcsec, 2) }}″
        </dd>

        <dt class="text-slate-500 dark:text-slate-400">
          {{ t("capture.tracking.samples") }}
        </dt>
        <dd class="font-mono text-slate-700 dark:text-slate-200">
          {{ report.samples }} · {{ fmt(report.span_sec / 60, 0) }} min
        </dd>
      </dl>

      <!-- The one figure worth acting on. -->
      <div
        v-if="report.max_unguided_sec > 0"
        class="rounded-md bg-brand-50 px-2 py-1.5 dark:bg-brand-500/10"
      >
        <p class="text-xs text-brand-700 dark:text-brand-300">
          {{
            t("capture.tracking.maxSub", {
              seconds: fmt(report.max_unguided_sec, 0),
            })
          }}
        </p>
      </div>

      <ul
        v-if="report.warnings?.length"
        class="space-y-0.5 text-[11px] text-amber-600 dark:text-amber-400"
      >
        <li v-for="w in report.warnings" :key="w">· {{ w }}</li>
      </ul>
    </div>

    <!-- Measurement running but recording nothing is the confusing case; name the reason rather
         than leaving an empty panel that reads as a broken feature. -->
    <p
      v-if="measuring && !measuring.recorded && measuring.attempts > 0"
      class="text-[11px] text-amber-600 dark:text-amber-400"
    >
      {{ t("capture.tracking.solveFailing", { attempts: measuring.attempts }) }}
      <span v-if="measuring.last_error" class="block font-mono text-slate-400">
        {{ measuring.last_error }}
      </span>
    </p>
  </div>
</template>
