<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { StarCatalogInfo } from "@/types";
import {
  compact,
  effectiveTemperatureK,
  formatDec,
  formatRA,
  lightYears,
  solarLuminosity,
  spectralClass,
} from "@/utils/starInfo";

// Everything the catalogue knows about one star, rendered once and used by both viewers — the 2D
// label overlay and the 3D field map. The two show the same star from different places, and a
// reader comparing them must not find two different cards.
//
// Only the catalogue row lives here. The title, where the card is positioned, and any rows that are
// specific to one viewer (a deep-sky object's type and size, say) belong to the caller and go
// through the slots.

const props = defineProps<{
  info: StarCatalogInfo | null | undefined;
  title: string;
  secondary?: string;
  titleClass?: string;
  // magEstimate is this frame's own photometric magnitude, shown only when the catalogue has none —
  // it is a weaker claim and is labelled differently so the two are never confused.
  magEstimate?: number | null;
}>();

const { t, te } = useI18n();

// A star's distance is the one figure people actually want, and light years is the unit they think
// in — but parsecs are what the catalogue measured, so both are shown.
const distanceText = computed(() => {
  const ly = lightYears(props.info);
  if (ly === null) return "";
  return t("stars.info.distanceValue", {
    ly: compact(ly),
    pc: compact(props.info!.dist_pc!),
  });
});

const spectralText = computed(() => {
  const raw = props.info?.spect;
  if (!raw) return "";
  const cls = spectralClass(props.info);
  const key = `stars.spectral.${cls}`;
  return cls && te(key) ? `${raw} — ${t(key)}` : raw;
});

const temperatureText = computed(() => {
  const k = effectiveTemperatureK(props.info);
  return k === null ? "" : t("stars.info.approxK", { k: compact(k) });
});

const luminosityText = computed(() => {
  const l = solarLuminosity(props.info);
  if (l === null) return "";
  return t("stars.info.luminosityValue", {
    abs: props.info!.absmag!.toFixed(2),
    sun: compact(l),
  });
});

// Positive radial velocity is motion away from us; the sign alone is unreadable, the word is not.
const velocityText = computed(() => {
  const rv = props.info?.rv_km_s;
  if (rv === null || rv === undefined) return "";
  const dir = rv >= 0 ? t("stars.info.receding") : t("stars.info.approaching");
  return `${rv.toFixed(1)} km/s — ${dir}`;
});

const coordsText = computed(() => {
  const ra = formatRA(props.info?.ra_deg);
  const dec = formatDec(props.info?.dec_deg);
  return ra && dec ? `${ra} ${dec}` : "";
});

const hasCatalogueMag = computed(
  () => props.info?.mag !== undefined && props.info?.mag !== null,
);
</script>

<template>
  <div>
    <div class="font-medium" :class="titleClass || 'text-slate-100'">
      {{ title }}
    </div>
    <div v-if="secondary" class="text-slate-400">{{ secondary }}</div>
    <dl class="mt-1 grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 tabular-nums">
      <!-- Caller-specific rows first: they say what KIND of thing this is, which frames the rest. -->
      <slot name="lead" />

      <template v-if="hasCatalogueMag">
        <dt class="text-slate-500">{{ t("stars.info.mag") }}</dt>
        <dd>{{ info!.mag!.toFixed(2) }}</dd>
      </template>
      <template v-else-if="magEstimate !== null && magEstimate !== undefined && magEstimate < 90">
        <dt class="text-slate-500">{{ t("stars.info.magEst") }}</dt>
        <dd>{{ magEstimate.toFixed(1) }}</dd>
      </template>

      <template v-if="distanceText">
        <dt class="text-slate-500">{{ t("stars.info.distance") }}</dt>
        <dd>{{ distanceText }}</dd>
      </template>
      <template v-if="spectralText">
        <dt class="text-slate-500">{{ t("stars.info.spectral") }}</dt>
        <dd>{{ spectralText }}</dd>
      </template>
      <template v-if="temperatureText">
        <dt class="text-slate-500">{{ t("stars.info.temperature") }}</dt>
        <dd>{{ temperatureText }}</dd>
      </template>
      <template v-if="luminosityText">
        <dt class="text-slate-500">{{ t("stars.info.luminosity") }}</dt>
        <dd>{{ luminosityText }}</dd>
      </template>
      <template v-if="velocityText">
        <dt class="text-slate-500">{{ t("stars.info.velocity") }}</dt>
        <dd>{{ velocityText }}</dd>
      </template>
      <template v-if="coordsText">
        <dt class="text-slate-500">{{ t("stars.info.coords") }}</dt>
        <dd>{{ coordsText }}</dd>
      </template>

      <!-- Rows that only make sense in one viewer — the 3D map's depth provenance, for instance. -->
      <slot name="trail" />
    </dl>
    <slot />
  </div>
</template>
