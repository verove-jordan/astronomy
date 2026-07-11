<script setup lang="ts">
// The agent's pending confirmation card: either an approve/reject for a state-changing action, or a
// multiple-choice question (e.g. "which fix do you want to apply?"). Emits the user's answer; the store
// forwards it to the server-side loop, which then proceeds.
import { useI18n } from "vue-i18n";
import { btnPrimary, btnGhost } from "@/constants/styles";
import type { PendingConfirm } from "@/stores/agent";

defineProps<{ confirm: PendingConfirm }>();
const emit = defineEmits<{ respond: [approve: boolean, choice?: string] }>();
const { t } = useI18n();
</script>

<template>
  <div
    class="rounded-lg border-2 border-amber-400/60 bg-amber-50 p-3 dark:border-amber-500/40 dark:bg-amber-900/20"
    data-demo="agent-confirm"
  >
    <template v-if="confirm.kind === 'confirm'">
      <p class="mb-1 text-sm font-medium">{{ t("agent.confirm.title") }}</p>
      <p class="text-sm text-slate-700 dark:text-slate-200">
        <span class="font-medium text-sky-600 dark:text-sky-400">{{
          confirm.tool
        }}</span>
        <span v-if="confirm.question"> — {{ confirm.question }}</span>
      </p>
      <pre
        v-if="confirm.args && confirm.args !== '{}'"
        class="mt-1 overflow-x-auto rounded bg-white/60 p-1.5 text-xs dark:bg-slate-900/60"
        >{{ confirm.args }}</pre
      >
      <div class="mt-2 flex gap-2">
        <button :class="btnPrimary" @click="emit('respond', true)">
          {{ t("agent.confirm.approve") }}
        </button>
        <button :class="btnGhost" @click="emit('respond', false)">
          {{ t("agent.confirm.reject") }}
        </button>
      </div>
    </template>

    <template v-else>
      <p class="mb-2 text-sm font-medium">{{ confirm.question }}</p>
      <div class="flex flex-wrap gap-2">
        <button
          v-for="o in confirm.options"
          :key="o.id"
          :class="btnGhost"
          :title="o.detail"
          @click="emit('respond', true, o.id)"
        >
          {{ o.label }}
        </button>
      </div>
    </template>
  </div>
</template>
