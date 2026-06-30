<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useGotoStore } from "@/stores/goto";
import AlignStarCard from "@/components/Goto/AlignStarCard.vue";
import ScoreBadge from "@/components/Common/ScoreBadge.vue";
import { card, btnGhost } from "@/constants/styles";

const { t } = useI18n();
const store = useGotoStore();

const stars = computed(() => store.stars);

// Only the most-recently-centered star offers Undo, so stepping back is unambiguous.
const lastAcceptedOrder = computed(() => {
  const accepted = stars.value.filter((s) => s.status === "accepted");
  return accepted.length ? accepted[accepted.length - 1].order : -1;
});
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
      {{ store.warnings[0] }}
    </p>

    <div v-if="stars.length" class="mt-3 space-y-2">
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
