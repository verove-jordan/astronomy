<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Pill from "@/components/Common/Pill.vue";
import { scoreTier, scoreTierPill } from "@/constants/styles";

const props = defineProps<{ score: number }>();
const { t } = useI18n();
const tier = computed(() => scoreTier(props.score));
</script>

<template>
  <Pill :color-class="scoreTierPill[tier]">
    {{ t("tonight.tiers." + tier) }} · {{ Math.round(score) }}
  </Pill>
</template>
