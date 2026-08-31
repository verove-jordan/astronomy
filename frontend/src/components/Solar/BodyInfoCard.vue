<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";

import Pill from "@/components/Common/Pill.vue";
import type { SolarBody, SolarBodyState } from "@/types";

// What is known about the body under the pointer.
//
// The figures come from the engine's snapshot, not from the renderer: the picture is drawn in a
// warped, exaggerated space and these are the real numbers. When the snapshot has not caught up with
// the scrubber the physical facts still show — a radius does not depend on the time.

const props = defineProps<{
  body: SolarBody;
  state: SolarBodyState | null;
  pinned: boolean;
  /** How far the engine's snapshot is behind the instant on screen, in simulated milliseconds. */
  lagMs?: number;
  /** The instant the snapshot actually describes. */
  stateAtMs?: number;
}>();

defineEmits<{ (e: "unpin"): void }>();

const { t } = useI18n();

const KM_PER_AU = 149597870.7;

function au(v: number | undefined): string {
  if (v === undefined || !Number.isFinite(v)) return "—";
  if (v < 0.01)
    return `${(v * KM_PER_AU).toLocaleString(undefined, { maximumFractionDigits: 0 })} km`;
  return `${v.toFixed(3)} AU`;
}

function deg(v: number | undefined, digits = 1): string {
  return v === undefined || !Number.isFinite(v) ? "—" : `${v.toFixed(digits)}°`;
}

const radiusLabel = computed(
  () =>
    `${props.body.radius_km.toLocaleString(undefined, { maximumFractionDigits: 0 })} km`,
);

const massLabel = computed(() => {
  const m = props.body.mass_kg;
  if (!m) return null;
  const exp = Math.floor(Math.log10(m));
  return `${(m / 10 ** exp).toFixed(2)}×10^${exp} kg`;
});

/** The rotation period, said the way people say it — and flagged when it runs backwards. */
const rotationLabel = computed(() => {
  const hours = (360 / props.body.pole.w_dot) * 24;
  const retro = hours < 0;
  const abs = Math.abs(hours);
  const value =
    abs >= 48
      ? `${(abs / 24).toFixed(2)} ${t("solarsystem.info.days")}`
      : `${abs.toFixed(2)} h`;
  return retro ? `${value} · ${t("solarsystem.info.retrograde")}` : value;
});

const apparentSize = computed(() => {
  const a = props.state?.angular_diameter_arcsec;
  if (!a) return "—";
  return a >= 120 ? `${(a / 60).toFixed(1)}′` : `${a.toFixed(1)}″`;
});

/**
 * The readout is a snapshot at an instant, and at high speed the clock outruns the engine. Rather
 * than show last moment's geometry as if it were this one's, say which moment it is — but only once
 * the gap is big enough to matter, so the common case stays uncluttered.
 */
const asOf = computed(() => {
  const lag = Math.abs(props.lagMs ?? 0);
  if (lag < 2000 || !props.state || props.stateAtMs === undefined) return null;
  return new Date(props.stateAtMs).toISOString().replace("T", " ").slice(0, 19);
});

const tierClass = computed(
  () =>
    ({
      fitted: "bg-emerald-500/15 text-emerald-300 ring-1 ring-emerald-500/30",
      mean: "bg-sky-500/15 text-sky-300 ring-1 ring-sky-500/30",
      sampled: "bg-amber-500/15 text-amber-300 ring-1 ring-amber-500/30",
    })[props.body.tier] ?? "bg-slate-500/15 text-slate-300",
);
</script>

<template>
  <div class="space-y-3" data-demo="solar-info">
    <div class="flex items-start justify-between gap-2">
      <div class="flex items-center gap-2">
        <span
          class="h-3 w-3 shrink-0 rounded-full"
          :style="{ backgroundColor: body.colour }"
          aria-hidden="true"
        />
        <h3 class="text-base font-semibold text-slate-100">
          {{ t(`solarsystem.bodies.${body.key}`) }}
        </h3>
      </div>
      <button
        v-if="pinned"
        type="button"
        class="rounded px-1 text-slate-400 hover:text-slate-100"
        :aria-label="t('solarsystem.info.unpin')"
        @click="$emit('unpin')"
      >
        ✕
      </button>
    </div>

    <div class="flex flex-wrap gap-1.5">
      <Pill :color-class="'bg-slate-700/60 text-slate-200'">{{
        t(`solarsystem.kinds.${body.kind}`)
      }}</Pill>
      <Pill v-if="body.parent" :color-class="'bg-slate-700/60 text-slate-200'">
        {{
          t("solarsystem.info.orbits", {
            body: t(`solarsystem.bodies.${body.parent}`),
          })
        }}
      </Pill>
      <Pill
        :color-class="tierClass"
        :title="t(`solarsystem.tiers.${body.tier}Hint`)"
      >
        {{ t(`solarsystem.tiers.${body.tier}`) }}
      </Pill>
    </div>

    <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
      <dt class="text-slate-400">{{ t("solarsystem.info.radius") }}</dt>
      <dd class="tabular-nums text-slate-200">{{ radiusLabel }}</dd>

      <template v-if="massLabel">
        <dt class="text-slate-400">{{ t("solarsystem.info.mass") }}</dt>
        <dd class="tabular-nums text-slate-200">{{ massLabel }}</dd>
      </template>

      <dt class="text-slate-400">{{ t("solarsystem.info.rotation") }}</dt>
      <dd class="tabular-nums text-slate-200">{{ rotationLabel }}</dd>

      <template v-if="state">
        <dt class="text-slate-400">{{ t("solarsystem.info.axialTilt") }}</dt>
        <dd class="tabular-nums text-slate-200">
          {{ deg(state.axial_tilt_deg, 2) }}
        </dd>

        <dt class="text-slate-400">{{ t("solarsystem.info.distanceSun") }}</dt>
        <dd class="tabular-nums text-slate-200">
          {{ au(state.helio_dist_au) }}
        </dd>

        <dt class="text-slate-400">
          {{ t("solarsystem.info.distanceEarth") }}
        </dt>
        <dd class="tabular-nums text-slate-200">{{ au(state.geo_dist_au) }}</dd>

        <dt class="text-slate-400">{{ t("solarsystem.info.magnitude") }}</dt>
        <dd class="tabular-nums text-slate-200">
          {{ state.magnitude > 90 ? "—" : state.magnitude.toFixed(1) }}
        </dd>

        <dt class="text-slate-400">{{ t("solarsystem.info.apparentSize") }}</dt>
        <dd class="tabular-nums text-slate-200">{{ apparentSize }}</dd>

        <template v-if="body.kind !== 'star'">
          <dt class="text-slate-400">
            {{ t("solarsystem.info.illuminated") }}
          </dt>
          <dd class="tabular-nums text-slate-200">
            {{ (state.illum_fraction * 100).toFixed(0) }} %
          </dd>

          <dt class="text-slate-400">{{ t("solarsystem.info.elongation") }}</dt>
          <dd class="tabular-nums text-slate-200">
            {{ deg(state.elongation_deg) }}
          </dd>
        </template>

        <template v-if="state.ring_open_deg !== undefined">
          <dt class="text-slate-400">{{ t("solarsystem.info.ringOpen") }}</dt>
          <dd class="tabular-nums text-slate-200">
            {{ deg(state.ring_open_deg, 2) }}
          </dd>
        </template>

        <dt class="text-slate-400">{{ t("solarsystem.info.altAz") }}</dt>
        <dd
          class="tabular-nums"
          :class="state.up ? 'text-success-300' : 'text-slate-400'"
        >
          {{ deg(state.alt_deg) }} / {{ deg(state.az_deg) }}
          <span class="ml-1 text-xs">{{
            state.up ? t("solarsystem.info.up") : t("solarsystem.info.below")
          }}</span>
        </dd>
      </template>
    </dl>

    <p v-if="asOf" class="text-xs text-warning-400">
      {{ t("solarsystem.info.asOf", { at: asOf }) }}
    </p>

    <p class="text-xs leading-relaxed text-slate-500">{{ body.source }}</p>
  </div>
</template>
