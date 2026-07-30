<script setup lang="ts">
// A single inline-SVG icon per event kind. Monochrome (currentColor) so it inherits the surrounding
// pill/text colour and themes cleanly in light & dark mode at any size. Vector → crisp on the dense
// calendar. One <svg>, inner shapes chosen by kind.
defineProps<{ kind: string }>();
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    class="inline-block"
  >
    <!-- Solar eclipse: the Moon's disc sliding over the Sun's ring -->
    <template v-if="kind === 'solar_eclipse'">
      <circle cx="10.5" cy="13" r="6" />
      <circle cx="16" cy="9" r="5.5" fill="currentColor" stroke="none" />
    </template>

    <!-- Lunar eclipse: Moon half-swallowed by Earth's umbra -->
    <template v-else-if="kind === 'lunar_eclipse'">
      <circle cx="12" cy="12" r="6.5" />
      <path
        d="M12 5.5 a6.5 6.5 0 0 1 0 13 z"
        fill="currentColor"
        stroke="none"
      />
    </template>

    <!-- Conjunction: two bodies meeting -->
    <template v-else-if="kind === 'conjunction'">
      <circle cx="9.5" cy="12" r="4.2" />
      <circle cx="15" cy="12" r="4.2" />
    </template>

    <!-- Moon & planet: a crescent next to a planet -->
    <template v-else-if="kind === 'planet_moon'">
      <path
        d="M17 14 A6 6 0 1 1 11 8 A4.6 4.6 0 0 0 17 14 Z"
        fill="currentColor"
        stroke="none"
      />
      <circle cx="6.5" cy="7" r="1.8" fill="currentColor" stroke="none" />
    </template>

    <!-- Opposition: Sun — Earth — planet in a line -->
    <template v-else-if="kind === 'opposition'">
      <circle cx="4" cy="12" r="2.6" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12" r="2" />
      <circle cx="20" cy="12" r="1.6" fill="currentColor" stroke="none" />
      <line x1="6.6" y1="12" x2="10" y2="12" />
      <line x1="14" y1="12" x2="18.4" y2="12" />
    </template>

    <!-- Greatest elongation: planet at its widest angle from the Sun -->
    <template v-else-if="kind === 'elongation'">
      <circle cx="6" cy="17" r="2.4" fill="currentColor" stroke="none" />
      <line x1="6" y1="17" x2="19" y2="17" />
      <line x1="6" y1="17" x2="16" y2="7" />
      <circle cx="16" cy="7" r="1.7" fill="currentColor" stroke="none" />
    </template>

    <!-- Moon phase: a crescent -->
    <template v-else-if="kind === 'moon_phase'">
      <path
        d="M21 12.79 A9 9 0 1 1 11.21 3 A7 7 0 0 0 21 12.79 Z"
        fill="currentColor"
        stroke="none"
      />
    </template>

    <!-- Supermoon: a big bright full Moon -->
    <template v-else-if="kind === 'supermoon'">
      <circle cx="12" cy="12" r="6" fill="currentColor" stroke="none" />
      <line x1="12" y1="1.5" x2="12" y2="3.6" />
      <line x1="12" y1="20.4" x2="12" y2="22.5" />
      <line x1="1.5" y1="12" x2="3.6" y2="12" />
      <line x1="20.4" y1="12" x2="22.5" y2="12" />
    </template>

    <!-- Equinox: the Sun sitting on the horizon -->
    <template v-else-if="kind === 'equinox'">
      <line x1="3" y1="15" x2="21" y2="15" />
      <path d="M7 15 a5 5 0 0 1 10 0" />
      <line x1="12" y1="4" x2="12" y2="6" />
    </template>

    <!-- Solstice: the Sun at its extreme, blazing -->
    <template v-else-if="kind === 'solstice'">
      <circle cx="12" cy="12" r="3.6" />
      <line x1="12" y1="2.5" x2="12" y2="5" />
      <line x1="12" y1="19" x2="12" y2="21.5" />
      <line x1="2.5" y1="12" x2="5" y2="12" />
      <line x1="19" y1="12" x2="21.5" y2="12" />
      <line x1="5.2" y1="5.2" x2="7" y2="7" />
      <line x1="17" y1="17" x2="18.8" y2="18.8" />
      <line x1="17" y1="7" x2="18.8" y2="5.2" />
      <line x1="5.2" y1="18.8" x2="7" y2="17" />
    </template>

    <!-- Perihelion / aphelion: a body on its elliptical orbit around the Sun -->
    <template v-else-if="kind === 'perihelion' || kind === 'aphelion'">
      <ellipse cx="12" cy="12" rx="9" ry="5" />
      <circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none" />
      <circle
        :cx="kind === 'perihelion' ? 3 : 21"
        cy="12"
        r="1.9"
        fill="currentColor"
        stroke="none"
      />
    </template>

    <!-- Meteor shower: streaks from a radiant -->
    <template v-else-if="kind === 'meteor_shower'">
      <circle cx="4.5" cy="6" r="1.3" fill="currentColor" stroke="none" />
      <line x1="4.5" y1="6" x2="11" y2="12.5" />
      <line x1="10" y1="5" x2="14.5" y2="9.5" />
      <circle cx="13" cy="14" r="1.1" fill="currentColor" stroke="none" />
      <line x1="13" y1="14" x2="19" y2="20" />
    </template>

    <!-- Comet: bright head, swept tail -->
    <template v-else-if="kind === 'comet'">
      <circle cx="16" cy="8" r="3" fill="currentColor" stroke="none" />
      <line x1="14" y1="10" x2="5" y2="19" />
      <line x1="16.5" y1="11" x2="9" y2="19" />
      <line x1="12.6" y1="9.4" x2="4" y2="16" />
    </template>

    <!-- Satellite transit: a satellite crossing a disc -->
    <template v-else-if="kind === 'satellite_transit'">
      <circle cx="12" cy="12" r="7" />
      <rect
        x="10.4"
        y="10.7"
        width="3.2"
        height="2.6"
        rx="0.4"
        fill="currentColor"
        stroke="none"
      />
      <rect
        x="6.4"
        y="11.2"
        width="3"
        height="1.6"
        fill="currentColor"
        stroke="none"
      />
      <rect
        x="14.6"
        y="11.2"
        width="3"
        height="1.6"
        fill="currentColor"
        stroke="none"
      />
    </template>

    <!-- Fallback: a simple star -->
    <template v-else>
      <circle cx="12" cy="12" r="3" fill="currentColor" stroke="none" />
    </template>
  </svg>
</template>
