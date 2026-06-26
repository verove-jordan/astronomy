<script setup lang="ts">
import { useI18n } from "vue-i18n";

const { t } = useI18n();
defineProps<{ sizeClass?: string }>();
</script>

<template>
  <!-- Square brand mark; object-contain keeps the intrinsic aspect ratio (future-proofs a wordmark). -->
  <img
    src="/logo.png"
    :alt="t('app.logoAlt')"
    :class="['shrink-0 object-contain', sizeClass || 'h-9 w-9']"
  />
</template>
