<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import Pill from "@/components/Common/Pill.vue";
import { statusPill } from "@/constants/styles";

const props = defineProps<{ status: string }>();
const { t } = useI18n();

const cls = computed(() => statusPill[props.status] || statusPill.queued);
const label = computed(() => t(`status.${props.status}`));
</script>

<template>
  <Pill :color-class="cls" role="status">{{ label }}</Pill>
</template>
