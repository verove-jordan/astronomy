<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import AlignStarCard from "@/components/Goto/AlignStarCard.vue";
import ScoreBadge from "@/components/Common/ScoreBadge.vue";
import { card, btnGhost } from "@/constants/styles";
import type { GotoStar, GotoWarning } from "@/types";

const { t } = useI18n();
const store = useGotoStore();

const stars = computed(() => store.stars);

// Two-phase routines (Celestron EQ) group the sequence under "Alignment" / "Calibration" headers;
// single-phase profiles render the flat list. Calibration stars are numbered C1..C4 to match the
// sky-map markers and the hand-controller procedure.
const hasPhases = computed(() => stars.value.some((s) => s.phase));
const alignStars = computed(() =>
  stars.value.filter((s) => s.phase !== "calibration"),
);
const calibStars = computed(() =>
  stars.value.filter((s) => s.phase === "calibration"),
);
function seqLabel(s: GotoStar): string {
  if (s.phase === "calibration") return `C${s.order - store.alignCount}`;
  return String(s.order);
}

// Only the most-recently-centered star offers Undo, so stepping back is unambiguous.
const lastAcceptedOrder = computed(() => {
  const accepted = stars.value.filter((s) => s.status === "accepted");
  return accepted.length ? accepted[accepted.length - 1].order : -1;
});

// warningText renders a structured plan warning. `side` is the bare translated side word
// (calib_same_side); `sideClause` is the optional " on the … side of the meridian" fragment
// (few_stars), empty when no meridian rule applies so the sentence still reads naturally.
function warningText(w: GotoWarning): string {
  const sideWord = w.side ? t(`goto.card.${w.side}`) : "";
  const sideClause = w.side ? t("goto.warnings.side", { side: sideWord }) : "";
  return t(`goto.warnings.${w.code}`, {
    available: w.available ?? 0,
    requested: w.requested ?? 0,
    min_alt: Math.round(w.min_alt ?? 0),
    max_alt: Math.round(w.max_alt ?? 0),
    count: w.count ?? 0,
    side: sideWord,
    sideClause,
  });
}
</script>

<template>
  <div :class="card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3 class="text-sm font-semibold text-slate-700 dark:text-slate-200">
        {{ t("goto.sequence.title") }}
      </h3>
      <div class="flex items-center gap-3">
        <div class="flex items-center gap-1.5 text-xs text-slate-400">
          <span>{{ t("goto.quality.label") }}</span>
          <ScoreBadge :score="store.quality" />
        </div>
        <button
          v-if="store.accepted.length || store.rejected.length"
          :class="[btnGhost, 'px-2 py-1 text-xs']"
          @click="store.resetSequence()"
        >
          {{ t("goto.controls.reset") }}
        </button>
      </div>
    </div>

    <p
      v-if="store.warnings.length"
      class="mt-2 text-xs text-amber-600 dark:text-amber-400"
    >
      {{ warningText(store.warnings[0]) }}
    </p>

    <div v-if="stars.length && hasPhases" class="mt-3 space-y-2">
      <h4
        class="text-[11px] font-semibold uppercase tracking-wide text-slate-400"
      >
        {{ t("goto.sequence.phase.align") }}
      </h4>
      <AlignStarCard
        v-for="s in alignStars"
        :key="s.name"
        :star="s"
        :seq-label="seqLabel(s)"
        :can-undo="s.order === lastAcceptedOrder"
        @accept="store.accept"
        @skip="store.skip"
        @undo="store.undo"
      />
      <template v-if="calibStars.length">
        <h4
          class="pt-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400"
        >
          {{ t("goto.sequence.phase.calibration") }}
        </h4>
        <AlignStarCard
          v-for="s in calibStars"
          :key="s.name"
          :star="s"
          :seq-label="seqLabel(s)"
          :can-undo="s.order === lastAcceptedOrder"
          @accept="store.accept"
          @skip="store.skip"
          @undo="store.undo"
        />
      </template>
    </div>
    <div v-else-if="stars.length" class="mt-3 space-y-2">
      <AlignStarCard
        v-for="s in stars"
        :key="s.name"
        :star="s"
        :can-undo="s.order === lastAcceptedOrder"
        @accept="store.accept"
        @skip="store.skip"
        @undo="store.undo"
      />
    </div>
    <p v-else-if="!store.loading" class="mt-4 text-sm text-slate-400">
      {{ t("goto.sequence.empty") }}
    </p>
  </div>
</template>
