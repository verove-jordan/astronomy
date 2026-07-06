<script setup lang="ts">
// The transparent tool-activity log for one assistant turn: collapsed it shows the tools the agent
// called as compact chips; expanded it reveals, per step, the model's reasoning, the tool inputs
// (args) and the tool outputs. Fed by the streamed AgentStep[] on an assistant message.
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import { fileUrl } from "@/services/api";
import type { AgentStep } from "@/stores/agent";

const props = defineProps<{
  steps: AgentStep[];
  streaming?: boolean;
  defaultOpen?: boolean; // supervised-finish chat opens expanded so the per-pass previews are visible
}>();
const { t } = useI18n();
const open = ref(props.defaultOpen ?? false);

const toolCalls = computed(() =>
  props.steps.filter((s) => s.kind === "tool_call"),
);
</script>

<template>
  <div
    v-if="steps.length"
    class="rounded-lg border border-slate-200 bg-slate-50 text-sm dark:border-slate-700 dark:bg-slate-800/40"
    data-demo="agent-activity"
  >
    <button
      class="flex w-full flex-wrap items-center gap-2 px-3 py-2 text-left"
      @click="open = !open"
    >
      <span class="text-slate-400">{{ open ? "▾" : "▸" }}</span>
      <span class="font-medium">{{ t("agent.activity.title") }}</span>
      <span class="text-xs text-slate-500 dark:text-slate-400">
        {{ t("agent.activity.toolCount", { n: toolCalls.length }) }}
      </span>
      <span v-if="!open" class="flex flex-wrap gap-1">
        <span
          v-for="(tc, i) in toolCalls"
          :key="i"
          class="rounded bg-sky-500/15 px-1.5 py-0.5 text-xs text-sky-600 dark:text-sky-400"
        >
          {{ tc.tool }}
        </span>
      </span>
      <span
        v-if="streaming"
        class="ml-auto animate-pulse text-xs text-slate-400"
        >…</span
      >
    </button>

    <div
      v-if="open"
      class="space-y-2 border-t border-slate-200 px-3 py-2 dark:border-slate-700"
    >
      <template v-for="(s, i) in steps" :key="i">
        <div v-if="s.kind === 'thinking'" class="space-y-1">
          <p class="whitespace-pre-line text-slate-500 dark:text-slate-400">
            <span class="mr-1">💭</span>{{ s.text }}
          </p>
          <img
            v-if="s.preview"
            :src="fileUrl(s.preview)"
            :alt="t('agent.activity.preview')"
            class="max-h-64 rounded border border-slate-200 dark:border-slate-700"
          />
        </div>

        <div
          v-else-if="s.kind === 'tool_call'"
          class="rounded border border-slate-200 p-2 dark:border-slate-700"
        >
          <div class="flex items-center gap-2">
            <span class="font-medium text-sky-600 dark:text-sky-400">{{
              s.tool
            }}</span>
            <span
              v-if="s.mutating"
              class="rounded bg-amber-500/15 px-1 py-0.5 text-xs text-amber-600 dark:text-amber-400"
            >
              {{ t("agent.activity.mutating") }}
            </span>
          </div>
          <pre
            v-if="s.args && s.args !== '{}'"
            class="mt-1 overflow-x-auto rounded bg-slate-100 p-1.5 text-xs dark:bg-slate-900"
            >{{ s.args }}</pre
          >
          <pre
            v-if="s.output !== undefined"
            class="mt-1 max-h-48 overflow-auto rounded p-1.5 text-xs"
            :class="
              s.is_error
                ? 'bg-rose-500/10 text-rose-600 dark:text-rose-400'
                : 'bg-slate-100 dark:bg-slate-900'
            "
            >{{ s.output }}</pre
          >
          <p v-else class="mt-1 text-xs text-slate-400">
            {{ t("agent.activity.running") }}
          </p>
        </div>

        <p
          v-else-if="s.kind === 'confirm' || s.kind === 'ask'"
          class="text-slate-500 dark:text-slate-400"
        >
          <span class="mr-1">🔐</span>{{ s.tool || s.question }}
          <span v-if="s.resolved" class="text-slate-400">
            → {{ s.answer }}</span
          >
        </p>

        <p
          v-else-if="s.kind === 'error'"
          class="text-rose-600 dark:text-rose-400"
        >
          <span class="mr-1">⚠</span>{{ s.text }}
        </p>
      </template>
    </div>
  </div>
</template>
