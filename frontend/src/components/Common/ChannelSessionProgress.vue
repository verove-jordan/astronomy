<script setup lang="ts">
// Live per-night progress board for a cross-session run: one block per channel, one row per capture
// night with its calibrate → normalize stage chips (the pipeline's true per-night stages) and the
// photometric-normalization numbers as they stream in; register + stack are channel-level (all
// nights merge into one sequence) and sit on the channel header. States derive from the streamed
// stage previews + photom records; the active night pulses. Mounted only when ≥2 nights are expected.
import { useI18n } from "vue-i18n";
import { card } from "@/constants/styles";
import FilterChip from "@/components/Common/FilterChip.vue";
import Pill from "@/components/Common/Pill.vue";
import type { PhotomRecord, StagePreview } from "@/types";

const props = defineProps<{
  sessions: string[]; // expected capture nights (sorted)
  channels: string[]; // expected filters (canonical order)
  previews: StagePreview[];
  photom: PhotomRecord[];
  // filter → nights that actually have data (uneven channel sets — a night that shot only L/R).
  // Cells outside it render "—" instead of forever-pending chips. Absent/empty map = everything.
  coverage?: Record<string, string[]>;
  currentSession?: string;
}>();
const { t } = useI18n();

// A (channel, night) cell is covered when the plan/stream shows data for it — or when nothing is
// known about the channel at all (no plan yet: assume full coverage rather than dash everything).
function covered(filter: string, session: string): boolean {
  const nights = props.coverage?.[filter];
  if (!nights || nights.length === 0) return true;
  return nights.includes(session);
}

function has(stage: string, filter: string, session: string): boolean {
  return props.previews.some(
    (p) =>
      p.stage === stage &&
      (p.filter ?? "") === filter &&
      (p.session ?? "") === session,
  );
}
function photomFor(filter: string, session: string): PhotomRecord | undefined {
  return props.photom.find(
    (r) => (r.session ?? "") === session && r.label.startsWith(filter + " "),
  );
}
function stacked(filter: string): boolean {
  return props.previews.some(
    (p) => p.stage === "stacked" && (p.filter ?? "") === filter,
  );
}
// A night's CALIBRATE is done once its prenorm preview landed (rendered from the calibrated frames)
// or any later signal exists for the pair; NORMALIZE once its record or after-preview landed.
function calibrated(filter: string, session: string): boolean {
  return (
    has("prenorm", filter, session) ||
    normalizedDone(filter, session) ||
    stacked(filter)
  );
}
function normalizedDone(filter: string, session: string): boolean {
  return has("normalized", filter, session) || !!photomFor(filter, session);
}

const chipBase =
  "rounded px-1.5 py-0.5 text-[10px] font-medium transition-colors";
function chipClass(done: boolean, active: boolean): string {
  if (done) return `${chipBase} bg-success/10 text-success`;
  if (active) return `${chipBase} animate-pulse bg-brand-500/15 text-brand-500`;
  return `${chipBase} bg-slate-500/10 text-slate-400 dark:text-slate-500`;
}
function photomPills(rec: PhotomRecord): { key: string; cls: string }[] {
  const pills: { key: string; cls: string }[] = [];
  if (rec.ref)
    pills.push({
      key: "job.photomReference",
      cls: "bg-brand-500/10 text-brand-500",
    });
  else if (rec.applied)
    pills.push({ key: "job.photomApplied", cls: "bg-success/10 text-success" });
  else
    pills.push({
      key: "job.photomSkipped",
      cls: "bg-slate-500/10 text-slate-400",
    });
  if (rec.meta_seeded)
    pills.push({
      key: "job.photomMetaSeeded",
      cls: "bg-violet-500/10 text-violet-500",
    });
  if (rec.clamped)
    pills.push({ key: "job.photomClamped", cls: "bg-warning/10 text-warning" });
  if (rec.meta_disagree)
    pills.push({
      key: "job.photomMetaDisagree",
      cls: "bg-warning/10 text-warning",
    });
  return pills;
}
function photomLine(rec: PhotomRecord): string {
  const sign = rec.offset >= 0 ? "+" : "";
  return `×${rec.scale.toFixed(2)} ${sign}${rec.offset.toFixed(3)} · ${(rec.resid * 100).toFixed(1)}%`;
}
</script>

<template>
  <section :class="card" data-demo="session-progress">
    <h2 class="text-lg font-medium">{{ t("job.sessionsTitle") }}</h2>
    <p class="mb-3 text-sm text-slate-500 dark:text-slate-400">
      {{ t("job.sessionsHint") }}
    </p>
    <div class="space-y-4">
      <div v-for="f in channels" :key="f">
        <div class="mb-1 flex items-center gap-2">
          <FilterChip :filter="f" />
          <span class="ml-auto flex items-center gap-1">
            <span :class="chipClass(stacked(f), false)">{{
              t("job.stageRegister")
            }}</span>
            <span :class="chipClass(stacked(f), false)">{{
              t("job.stageStack")
            }}</span>
          </span>
        </div>
        <div class="space-y-1 pl-1">
          <div
            v-for="s in sessions"
            :key="s"
            class="flex flex-wrap items-center gap-2 text-sm"
          >
            <span
              class="w-28 shrink-0 text-xs"
              :class="
                currentSession === s && covered(f, s)
                  ? 'font-semibold text-brand-500'
                  : 'text-slate-500 dark:text-slate-400'
              "
            >
              {{ s || t("sessions.undated") }}
            </span>
            <span
              v-if="!covered(f, s)"
              class="text-xs text-slate-400 dark:text-slate-600"
              :title="t('job.noDataNight')"
            >
              —
            </span>
            <template v-else>
              <span
                :class="
                  chipClass(
                    calibrated(f, s),
                    currentSession === s && !calibrated(f, s),
                  )
                "
              >
                {{ t("job.stageCalibrate") }}
              </span>
              <span
                :class="
                  chipClass(
                    normalizedDone(f, s),
                    currentSession === s &&
                      calibrated(f, s) &&
                      !normalizedDone(f, s),
                  )
                "
              >
                {{ t("job.stageNormalize") }}
              </span>
              <template v-if="photomFor(f, s)">
                <span
                  class="text-xs tabular-nums text-slate-500 dark:text-slate-400"
                >
                  {{ photomLine(photomFor(f, s)!) }}
                </span>
                <Pill
                  v-for="p in photomPills(photomFor(f, s)!)"
                  :key="p.key"
                  :color-class="p.cls"
                >
                  {{ t(p.key) }}
                </Pill>
              </template>
            </template>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
