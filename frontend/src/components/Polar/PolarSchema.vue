<script setup lang="ts">
// PolarSchema renders a small schematic diagram for one tutorial step (1–6). Diagrams are inline SVG
// (a data-viz, like the reticle/charts) with only universal symbols; the prose lives in the tutorial.
defineProps<{ step: number }>();
</script>

<template>
  <svg
    viewBox="0 0 220 130"
    class="h-32 w-full rounded-md border border-slate-700 bg-slate-950"
    aria-hidden="true"
  >
    <!-- 1 · Why polar alignment: mount RA axis parallel to Earth's axis, pointing at the pole star -->
    <g
      v-if="step === 1"
      fill="none"
      stroke="#5eead4"
      stroke-width="1.5"
      stroke-linecap="round"
    >
      <circle cx="55" cy="85" r="24" stroke-opacity="0.8" />
      <line x1="38" y1="100" x2="78" y2="58" stroke="#38bdf8" />
      <line x1="150" y1="105" x2="178" y2="60" stroke="#38bdf8" />
      <path d="M150 116 h28" stroke="#94a3b8" />
      <path d="M178 60 l-5 9 m5 -9 l-9 1" stroke="#38bdf8" />
      <path d="M78 58 l-5 9 m5 -9 l-9 1" stroke="#38bdf8" />
      <g fill="#fbbf24" stroke="none">
        <circle cx="185" cy="48" r="3" />
      </g>
      <text x="190" y="44" fill="#fcd34d" font-size="9">Polaris</text>
      <text x="36" y="124" fill="#94a3b8" font-size="8">Earth axis</text>
      <text x="150" y="126" fill="#94a3b8" font-size="8">RA axis ∥</text>
    </g>

    <!-- 2 · Collimate the polar scope: 3 grub screws; center a star, rotate RA 180°, halve the drift -->
    <g v-else-if="step === 2">
      <rect
        x="40"
        y="52"
        width="110"
        height="26"
        rx="4"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <circle
        cx="150"
        cy="65"
        r="22"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <g fill="#94a3b8" stroke="none">
        <circle cx="150" cy="45" r="2.5" />
        <circle cx="167" cy="74" r="2.5" />
        <circle cx="133" cy="74" r="2.5" />
      </g>
      <line
        x1="150"
        y1="55"
        x2="150"
        y2="75"
        stroke="#475569"
        stroke-width="0.8"
      />
      <line
        x1="140"
        y1="65"
        x2="160"
        y2="65"
        stroke="#475569"
        stroke-width="0.8"
      />
      <circle cx="156" cy="60" r="2.5" fill="#f97316" stroke="none" />
      <path
        d="M40 100 a34 16 0 0 1 70 0"
        fill="none"
        stroke="#38bdf8"
        stroke-width="1.2"
      />
      <path d="M110 100 l-7 -3 m7 3 l-4 7" fill="none" stroke="#38bdf8" />
      <text x="44" y="120" fill="#94a3b8" font-size="8">rotate RA 180°</text>
    </g>

    <!-- 3 · Mount setup: level the tripod, set the latitude scale, aim at true north -->
    <g v-else-if="step === 3" stroke-linecap="round">
      <path
        d="M70 100 l-18 18 m18 -18 l18 18 m-18 -18 v-20"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <rect
        x="52"
        y="66"
        width="36"
        height="10"
        rx="5"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.2"
      />
      <circle cx="70" cy="71" r="2.5" fill="#34d399" stroke="none" />
      <path
        d="M150 110 a44 44 0 0 0 -40 -44"
        fill="none"
        stroke="#38bdf8"
        stroke-width="1.2"
      />
      <line
        x1="150"
        y1="110"
        x2="110"
        y2="66"
        stroke="#475569"
        stroke-width="0.8"
      />
      <line
        x1="150"
        y1="110"
        x2="150"
        y2="66"
        stroke="#475569"
        stroke-width="0.8"
      />
      <text x="118" y="92" fill="#94a3b8" font-size="9">φ</text>
      <g fill="#fbbf24" stroke="none"><circle cx="150" cy="60" r="2.5" /></g>
      <text x="142" y="54" fill="#fcd34d" font-size="8">N</text>
    </g>

    <!-- 4 · Reticle illumination: shine a dim RED light into the objective; never white -->
    <g v-else-if="step === 4">
      <rect
        x="70"
        y="54"
        width="90"
        height="24"
        rx="4"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <circle
        cx="160"
        cy="66"
        r="16"
        fill="none"
        stroke="#f87171"
        stroke-width="1.5"
      />
      <line x1="148" y1="66" x2="172" y2="66" stroke="#f87171" />
      <line x1="160" y1="54" x2="160" y2="78" stroke="#f87171" />
      <g stroke="#f87171" stroke-width="1.2">
        <line x1="20" y1="55" x2="62" y2="60" />
        <line x1="20" y1="66" x2="62" y2="66" />
        <line x1="20" y1="77" x2="62" y2="72" />
      </g>
      <text x="14" y="92" fill="#fca5a5" font-size="8">red light</text>
      <g stroke="#e2e8f0" stroke-width="1.2">
        <circle cx="40" cy="104" r="6" fill="none" />
        <line x1="34" y1="110" x2="46" y2="98" stroke="#ef4444" />
      </g>
      <text x="50" y="108" fill="#94a3b8" font-size="8">no white</text>
    </g>

    <!-- 5 · Align: rotate RA to the clock position, then use the ALT/AZ bolts to centre the star -->
    <g v-else-if="step === 5">
      <circle
        cx="80"
        cy="65"
        r="34"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <line
        x1="46"
        y1="65"
        x2="114"
        y2="65"
        stroke="#475569"
        stroke-width="0.8"
      />
      <line
        x1="80"
        y1="31"
        x2="80"
        y2="99"
        stroke="#475569"
        stroke-width="0.8"
      />
      <circle
        cx="104"
        cy="48"
        r="7"
        fill="none"
        stroke="#f97316"
        stroke-width="2"
      />
      <circle cx="104" cy="48" r="2" fill="#fbbf24" stroke="none" />
      <g stroke="#38bdf8" stroke-width="1.4" fill="none">
        <path d="M150 50 v-16 m0 16 l-4 -6 m4 6 l4 -6" />
        <path d="M150 90 h22 m-22 0 l6 -4 m-6 4 l6 4" />
      </g>
      <text x="156" y="34" fill="#94a3b8" font-size="8">alt</text>
      <text x="150" y="104" fill="#94a3b8" font-size="8">az</text>
    </g>

    <!-- 6 · Verify / refine: cross-check with the hand controller, or a quick drift check -->
    <g v-else>
      <rect
        x="36"
        y="40"
        width="48"
        height="74"
        rx="6"
        fill="none"
        stroke="#5eead4"
        stroke-width="1.5"
      />
      <rect
        x="44"
        y="48"
        width="32"
        height="20"
        rx="2"
        fill="none"
        stroke="#38bdf8"
        stroke-width="1"
      />
      <text x="60" y="62" fill="#7dd3fc" font-size="6" text-anchor="middle">
        ALIGN
      </text>
      <g fill="#94a3b8" stroke="none">
        <circle cx="52" cy="80" r="2" />
        <circle cx="60" cy="80" r="2" />
        <circle cx="68" cy="80" r="2" />
        <circle cx="52" cy="92" r="2" />
        <circle cx="60" cy="92" r="2" />
        <circle cx="68" cy="92" r="2" />
      </g>
      <g fill="#fbbf24" stroke="none"><circle cx="150" cy="78" r="3" /></g>
      <g stroke="#38bdf8" stroke-width="1.4" fill="none">
        <path d="M150 60 v-12 m0 12 l-4 -6 m4 6 l4 -6" />
        <path d="M150 96 v12 m0 -12 l-4 6 m4 -6 l4 6" />
      </g>
      <text x="160" y="82" fill="#94a3b8" font-size="8">drift?</text>
    </g>
  </svg>
</template>
