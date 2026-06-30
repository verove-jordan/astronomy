<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { usePolarStore } from "@/stores/polar";

// PolarScopeReticle draws a SkyWatcher-style polar-scope reticle as seen through the scope: an engraved
// clock ring, the true-pole cross, the pole star's track, and the pole-star "bubble" at its live clock
// position, plus constellation direction guides. All geometry is derived from the polar store, so it
// updates live with time and instantly with the orientation toggle.
const { t } = useI18n();
const store = usePolarStore();

const CX = 200;
const CY = 200;
const R_RING = 172;
const R_TRACK = 120; // stylized (not to scale — Polaris is really ~0.7° from the pole)
const R_NUM = 148;
const R_GUIDE = 150;

// polar maps a clock angle (degrees, clockwise from 12 o'clock) + radius to SVG coords (y-down).
function polar(aDeg: number, r: number): { x: number; y: number } {
  const a = (aDeg * Math.PI) / 180;
  return { x: CX + r * Math.sin(a), y: CY - r * Math.cos(a) };
}

const ticks = computed(() =>
  Array.from({ length: 72 }, (_, i) => i * 5).map((a) => {
    const major = a % 30 === 0;
    const p1 = polar(a, R_RING);
    const p2 = polar(a, R_RING - (major ? 13 : 6));
    return { a, x1: p1.x, y1: p1.y, x2: p2.x, y2: p2.y, major };
  }),
);

const numerals = computed(() =>
  Array.from({ length: 12 }, (_, i) => {
    const h = i + 1; // 1..12 (12 at the top)
    const p = polar((h % 12) * 30, R_NUM);
    return { h, x: p.x, y: p.y };
  }),
);

const bubble = computed(() => polar(store.displayAngleDeg, R_TRACK));
const callout = computed(() => polar(store.displayAngleDeg, R_TRACK + 30));

// Constellation direction guides (small asterism glyphs) placed at their rotational clock angle.
type Glyph = {
  w: number;
  h: number;
  lines: string[];
  dots: [number, number][];
};
const GLYPHS: Record<string, Glyph> = {
  cassiopeia: {
    w: 30,
    h: 11,
    lines: ["0,0 7,11 15,4 23,11 30,0"],
    dots: [
      [0, 0],
      [7, 11],
      [15, 4],
      [23, 11],
      [30, 0],
    ],
  },
  bigDipper: {
    w: 30,
    h: 13,
    lines: ["0,12 1,4 9,3 10,11 16,13 23,11 30,13"],
    dots: [
      [0, 12],
      [1, 4],
      [9, 3],
      [10, 11],
      [16, 13],
      [23, 11],
      [30, 13],
    ],
  },
  crux: {
    w: 14,
    h: 18,
    lines: ["7,0 7,18", "0,8 14,10"],
    dots: [
      [7, 0],
      [7, 18],
      [0, 8],
      [14, 10],
    ],
  },
};

const NORTH_GUIDES = [
  { key: "cassiopeia", ra: 14.18, glyph: "cassiopeia" },
  { key: "bigDipper", ra: 165.93, glyph: "bigDipper" },
];
const SOUTH_GUIDES = [{ key: "crux", ra: 186.65, glyph: "crux" }];

const guides = computed(() => {
  const r = store.result;
  if (!r) return [];
  const list = r.hemisphere === "north" ? NORTH_GUIDES : SOUTH_GUIDES;
  return list.map((g) => {
    const p = polar(
      store.displayAngle(store.positionAngleForRA(g.ra)),
      R_GUIDE,
    );
    return { key: g.key, x: p.x, y: p.y, def: GLYPHS[g.glyph] };
  });
});

const poleStarLabel = computed(() =>
  store.result?.hemisphere === "south"
    ? t("tonight.polar.reticle.sigmaOct")
    : t("tonight.polar.reticle.polaris"),
);

const caption = computed(() => {
  if (store.mirror) return t("tonight.polar.reticle.mirrored");
  return store.invert
    ? t("tonight.polar.reticle.invertedView")
    : t("tonight.polar.reticle.erectView");
});
</script>

<template>
  <div class="rounded-lg border border-slate-700 bg-slate-950 p-3">
    <svg viewBox="0 0 400 400" class="mx-auto block w-full max-w-md">
      <defs>
        <radialGradient id="reticleGlass" cx="50%" cy="42%" r="65%">
          <stop offset="0%" stop-color="#0b1220" />
          <stop offset="100%" stop-color="#020617" />
        </radialGradient>
      </defs>

      <!-- Field of view -->
      <circle :cx="CX" :cy="CY" :r="R_RING + 14" fill="url(#reticleGlass)" />
      <!-- Engraved double ring -->
      <circle
        :cx="CX"
        :cy="CY"
        :r="R_RING"
        fill="none"
        stroke="#5eead4"
        stroke-opacity="0.7"
        stroke-width="1.5"
      />
      <circle
        :cx="CX"
        :cy="CY"
        :r="R_RING - 4"
        fill="none"
        stroke="#5eead4"
        stroke-opacity="0.35"
        stroke-width="1"
      />

      <!-- Hour / minute ticks -->
      <line
        v-for="tk in ticks"
        :key="tk.a"
        :x1="tk.x1"
        :y1="tk.y1"
        :x2="tk.x2"
        :y2="tk.y2"
        stroke="#5eead4"
        :stroke-opacity="tk.major ? 0.8 : 0.4"
        :stroke-width="tk.major ? 1.4 : 0.8"
      />

      <!-- Clock numerals -->
      <text
        v-for="n in numerals"
        :key="n.h"
        :x="n.x"
        :y="n.y"
        fill="#99f6e4"
        font-size="13"
        text-anchor="middle"
        dominant-baseline="central"
      >
        {{ n.h }}
      </text>

      <!-- Pole-star track -->
      <circle
        :cx="CX"
        :cy="CY"
        :r="R_TRACK"
        fill="none"
        stroke="#38bdf8"
        stroke-opacity="0.45"
        stroke-width="1"
        stroke-dasharray="4 4"
      />

      <!-- True celestial pole -->
      <g stroke="#f8fafc" stroke-opacity="0.8" stroke-width="1">
        <line :x1="CX - 16" :y1="CY" :x2="CX + 16" :y2="CY" />
        <line :x1="CX" :y1="CY - 16" :x2="CX" :y2="CY + 16" />
      </g>
      <circle
        :cx="CX"
        :cy="CY"
        r="3"
        fill="none"
        stroke="#f8fafc"
        stroke-opacity="0.8"
      />
      <text
        :x="CX + 10"
        :y="CY - 8"
        fill="#cbd5e1"
        font-size="9"
        text-anchor="start"
      >
        {{ t("tonight.polar.reticle.truePole") }}
      </text>

      <!-- Constellation direction guides -->
      <g v-for="g in guides" :key="g.key">
        <g
          :transform="`translate(${g.x - g.def.w / 2}, ${g.y - g.def.h / 2})`"
          fill="#fde68a"
          stroke="#fde68a"
        >
          <polyline
            v-for="(ln, i) in g.def.lines"
            :key="i"
            :points="ln"
            fill="none"
            stroke-opacity="0.7"
            stroke-width="1"
          />
          <circle
            v-for="(d, i) in g.def.dots"
            :key="`d${i}`"
            :cx="d[0]"
            :cy="d[1]"
            r="1.3"
            stroke="none"
          />
        </g>
        <text
          :x="g.x"
          :y="g.y + g.def.h / 2 + 11"
          fill="#fcd34d"
          font-size="9"
          text-anchor="middle"
        >
          {{ t(`tonight.polar.reticle.${g.key}`) }}
        </text>
      </g>

      <!-- Pole-star bubble (place the star here) -->
      <g v-if="store.result">
        <circle
          :cx="bubble.x"
          :cy="bubble.y"
          r="11"
          fill="none"
          stroke="#f97316"
          stroke-width="2"
          class="pulse"
        />
        <circle :cx="bubble.x" :cy="bubble.y" r="2.5" fill="#fbbf24" />
        <line
          :x1="bubble.x"
          :y1="bubble.y"
          :x2="callout.x"
          :y2="callout.y"
          stroke="#f97316"
          stroke-opacity="0.6"
          stroke-width="1"
        />
        <text
          :x="callout.x"
          :y="callout.y"
          fill="#fdba74"
          font-size="10"
          font-weight="600"
          text-anchor="middle"
          dominant-baseline="central"
        >
          {{ poleStarLabel }}
        </text>
      </g>
    </svg>

    <div
      class="mt-2 flex items-center justify-between text-xs text-slate-400"
      role="note"
    >
      <span>{{ caption }}</span>
      <span v-if="store.result" class="tabular-nums text-slate-300">
        {{ t("tonight.polar.reticle.placeHere") }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.pulse {
  animation: pulse 1.8s ease-in-out infinite;
  transform-origin: center;
}
@keyframes pulse {
  0%,
  100% {
    stroke-opacity: 1;
  }
  50% {
    stroke-opacity: 0.35;
  }
}
</style>
