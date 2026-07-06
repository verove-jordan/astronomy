<script setup lang="ts">
// The live, steerable conversation for a supervised finish. It binds to the job's backend turn (by id)
// and renders the exact same conversation model as the AstroAgent chat: the agent's per-pass activity
// (reasoning + defects + scores + preview image) via AgentActivity, the expensive-step confirmation via
// AgentConfirm, and — while the run is live — a composer to nudge it ("boost saturation") or stop it
// keeping the best pass. All wiring is reused from the agent store; this component only lays it out.
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useAgentStore } from "@/stores/agent";
import AgentActivity from "@/components/Common/AgentActivity.vue";
import AgentConfirm from "@/components/Common/AgentConfirm.vue";
import { card, input, btnPrimary, btnGhost } from "@/constants/styles";

const props = defineProps<{ turnId: string }>();
const { t } = useI18n();
const agent = useAgentStore();

const conv = computed(() => agent.conversationByTurn(props.turnId));
const live = computed(() => !!conv.value?.live);
const messages = computed(() => conv.value?.messages ?? []);
// Only surface the confirm card here when it belongs to THIS turn (pendingConfirm is app-wide).
const pending = computed(() =>
  agent.pendingConfirm?.turnId === props.turnId ? agent.pendingConfirm : null,
);

const draft = ref("");
const sending = ref(false);

async function send(): Promise<void> {
  const text = draft.value.trim();
  if (!text || sending.value) return;
  draft.value = "";
  sending.value = true;
  try {
    await agent.steerTurn(props.turnId, text);
  } finally {
    sending.value = false;
  }
}
async function stop(): Promise<void> {
  await agent.steerTurn(props.turnId, "", true);
}
function onConfirm(approve: boolean, choice?: string): void {
  void agent.respondConfirm(approve, choice);
}
</script>

<template>
  <section :class="card" data-demo="supervisor-chat">
    <div class="mb-2 flex items-center gap-2">
      <h2 class="text-lg font-medium">{{ t("supervisorChat.title") }}</h2>
      <span
        v-if="live"
        class="rounded bg-emerald-500/15 px-1.5 py-0.5 text-xs text-emerald-600 dark:text-emerald-400"
      >
        {{ t("supervisorChat.live") }}
      </span>
    </div>
    <p class="mb-3 text-sm text-slate-500 dark:text-slate-400">
      {{ t("supervisorChat.hint") }}
    </p>

    <div class="space-y-3">
      <template v-for="(m, i) in messages" :key="i">
        <AgentActivity
          v-if="m.role === 'assistant' && m.steps?.length"
          :steps="m.steps"
          :streaming="live"
          :default-open="true"
        />
        <p
          v-if="m.role === 'assistant' && m.text"
          class="text-sm text-slate-700 dark:text-slate-200"
        >
          {{ m.text }}
        </p>
        <div
          v-else-if="m.role === 'user'"
          class="ml-auto w-fit max-w-[85%] rounded-lg bg-brand-600/10 px-3 py-1.5 text-sm text-slate-800 dark:text-slate-100"
        >
          {{ m.text }}
        </div>
      </template>
    </div>

    <AgentConfirm
      v-if="pending"
      :confirm="pending"
      class="mt-3"
      @respond="onConfirm"
    />

    <div v-if="live" class="mt-3 space-y-2">
      <textarea
        v-model="draft"
        :class="[input, 'min-h-[60px] resize-y']"
        :placeholder="t('supervisorChat.placeholder')"
        data-demo="supervisor-steer"
        @keydown.enter.exact.prevent="send"
      />
      <div class="flex gap-2">
        <button
          :class="btnPrimary"
          :disabled="!draft.trim() || sending"
          @click="send"
        >
          {{ t("supervisorChat.send") }}
        </button>
        <button :class="btnGhost" data-demo="supervisor-stop" @click="stop">
          {{ t("supervisorChat.stop") }}
        </button>
      </div>
    </div>
  </section>
</template>
