<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useAgentStore } from "@/stores/agent";
import { readImageFileAsDataURL } from "@/utils/readImageFile";
import { ApiError } from "@/services/api";
import { card, btnPrimary, btnGhost, input } from "@/constants/styles";
import MarkdownText from "@/components/Common/MarkdownText.vue";
import ImageStatsPanel from "@/components/Common/ImageStatsPanel.vue";
import AgentActivity from "@/components/Common/AgentActivity.vue";
import AgentConfirm from "@/components/Common/AgentConfirm.vue";

const { t } = useI18n();
const agent = useAgentStore();

const draft = ref("");
const attachments = ref<string[]>([]); // pending image data URLs for the next turn
const selectedModel = ref("");
const sending = ref(false);
const error = ref("");
const dragOver = ref(false);
const copiedIdx = ref<number | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const transcriptEl = ref<HTMLElement | null>(null);

// The picker offers what the server advertises; falls back to the configured default id.
const modelOptions = computed(() =>
  agent.models.length ? agent.models : agent.model ? [agent.model] : [],
);

function initModel() {
  if (selectedModel.value && modelOptions.value.includes(selectedModel.value))
    return;
  selectedModel.value = agent.model || modelOptions.value[0] || "";
}
watch(() => [agent.model, agent.models] as const, initModel, {
  immediate: true,
});

// When switching conversations, reuse that conversation's model (if still available) and jump to the end.
watch(
  () => agent.activeId,
  () => {
    error.value = "";
    const m = agent.active?.model;
    if (m && modelOptions.value.includes(m)) selectedModel.value = m;
    void scrollToBottom();
  },
);
watch(
  () => [agent.activeMessages.length, sending.value] as const,
  () => void scrollToBottom(),
);
// Keep the transcript pinned to the bottom as steps stream in and as a confirmation appears.
const lastSteps = computed(
  () =>
    agent.activeMessages[agent.activeMessages.length - 1]?.steps?.length ?? 0,
);
watch(
  () => [lastSteps.value, agent.pendingConfirm?.callId] as const,
  () => void scrollToBottom(),
);

// Show the "thinking" placeholder only until the first step streams in (then the activity log takes over).
const showThinking = computed(() => agent.streaming && lastSteps.value === 0);

function onConfirm(approve: boolean, choice?: string) {
  void agent.respondConfirm(approve, choice);
}

onMounted(() => {
  void agent.refreshStatus();
});

const canSend = computed(
  () =>
    !sending.value &&
    !!selectedModel.value &&
    (draft.value.trim().length > 0 || attachments.value.length > 0),
);

async function addFiles(files: FileList | File[]) {
  for (const f of Array.from(files)) {
    if (!f.type.startsWith("image/")) continue;
    try {
      attachments.value.push(await readImageFileAsDataURL(f));
    } catch {
      /* skip an unreadable file */
    }
  }
}
function onFileChange(e: Event) {
  const el = e.target as HTMLInputElement;
  if (el.files) void addFiles(el.files);
  el.value = ""; // allow re-selecting the same file
}
function onDrop(e: DragEvent) {
  dragOver.value = false;
  if (e.dataTransfer?.files?.length) void addFiles(e.dataTransfer.files);
}
function removeAttachment(i: number) {
  attachments.value.splice(i, 1);
}

async function scrollToBottom() {
  await nextTick();
  const el = transcriptEl.value;
  if (el) el.scrollTop = el.scrollHeight;
}

async function send() {
  if (!canSend.value) return;
  error.value = "";
  const text = draft.value.trim();
  // A live supervised-finish conversation: the composer steers the running loop (folded into the next
  // pass) instead of starting a fresh chat turn.
  const conv = agent.active;
  if (conv?.live && conv.turnId) {
    draft.value = "";
    sending.value = true;
    try {
      await agent.steerTurn(conv.turnId, text);
    } catch (e) {
      error.value =
        e instanceof ApiError ? e.message : (e as Error).message || String(e);
    } finally {
      sending.value = false;
      await scrollToBottom();
    }
    return;
  }
  const images = [...attachments.value];
  draft.value = "";
  attachments.value = [];
  sending.value = true;
  await scrollToBottom();
  try {
    await agent.send(text, images, {
      model: selectedModel.value,
      maxTokens: 2048,
      temperature: 0.2,
    });
  } catch (e) {
    error.value =
      e instanceof ApiError ? e.message : (e as Error).message || String(e);
  } finally {
    sending.value = false;
    await scrollToBottom();
  }
}

function onKeydown(e: KeyboardEvent) {
  // Ctrl/Cmd+Enter sends; plain Enter inserts a newline.
  if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
    e.preventDefault();
    void send();
  }
}

function startNewChat() {
  agent.newChat();
  draft.value = "";
  attachments.value = [];
  error.value = "";
}
function openChat(id: string) {
  agent.selectChat(id);
}

async function copyText(text: string, idx: number) {
  try {
    await navigator.clipboard.writeText(text);
    copiedIdx.value = idx;
    setTimeout(() => {
      if (copiedIdx.value === idx) copiedIdx.value = null;
    }, 1500);
  } catch {
    /* clipboard unavailable */
  }
}

// relativeTime renders a compact age label (units are language-neutral; dates use the locale).
function relativeTime(ts: number): string {
  const s = Math.floor((Date.now() - ts) / 1000);
  if (s < 60) return "<1m";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d`;
  return new Date(ts).toLocaleDateString();
}

const rowClass = (role: string) =>
  role === "user" ? "flex justify-end" : "flex justify-start";
const bubbleClass = (role: string) =>
  role === "user"
    ? "max-w-[80%] rounded-lg bg-brand-600 px-3 py-2 text-white"
    : "rounded-lg bg-slate-800 px-3 py-2 text-slate-100";
const convItemClass = (id: string) =>
  "group flex items-center gap-2 rounded-md px-2 py-1.5 cursor-pointer " +
  (id === agent.activeId
    ? "bg-brand-600/20 text-brand-100 ring-1 ring-brand-500/40"
    : "text-slate-300 hover:bg-slate-700/50");
</script>

<template>
  <div class="space-y-4">
    <header class="flex flex-wrap items-start gap-3">
      <div class="min-w-0">
        <h1 class="text-xl font-bold text-brand-300">
          {{ t("astroAgent.title") }}
        </h1>
        <p class="text-sm text-slate-400">{{ t("astroAgent.subtitle") }}</p>
      </div>
      <div
        v-if="agent.available && modelOptions.length"
        class="ml-auto flex items-center gap-2"
      >
        <label class="text-xs text-slate-400">{{
          t("astroAgent.modelLabel")
        }}</label>
        <select v-model="selectedModel" :class="[input, 'w-auto sm:w-64']">
          <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
    </header>

    <!-- Guard: the nav link is gated on availability, but a deep link or a mid-session stop can still
         land here with the agent down. -->
    <div v-if="agent.checked && !agent.available" :class="card">
      <p class="font-medium text-slate-200">{{ t("astroAgent.notRunning") }}</p>
      <p class="mt-1 text-sm text-slate-400">
        {{ t("astroAgent.notRunningHint") }}
        <code class="rounded bg-slate-800 px-1.5 py-0.5 text-brand-300"
          >just run-ia-model</code
        >
      </p>
    </div>

    <div v-else class="flex flex-col gap-4 md:flex-row">
      <!-- Conversation history -->
      <aside class="flex shrink-0 flex-col gap-2 md:w-64">
        <button
          type="button"
          :class="[btnPrimary, 'w-full']"
          @click="startNewChat"
        >
          + {{ t("astroAgent.newChat") }}
        </button>
        <div
          class="px-1 pt-1 text-xs font-semibold uppercase tracking-wide text-slate-500"
        >
          {{ t("astroAgent.history") }}
        </div>
        <ul class="max-h-[24vh] space-y-1 overflow-y-auto md:max-h-[52vh]">
          <li
            v-if="!agent.orderedConversations.length"
            class="px-2 py-1 text-sm text-slate-500"
          >
            {{ t("astroAgent.noChats") }}
          </li>
          <li v-for="c in agent.orderedConversations" :key="c.id">
            <div :class="convItemClass(c.id)" @click="openChat(c.id)">
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm">
                  {{ c.title || t("astroAgent.newChat") }}
                </div>
                <div class="text-xs text-slate-500">
                  {{ relativeTime(c.updatedAt) }}
                </div>
              </div>
              <button
                type="button"
                class="shrink-0 rounded p-1 text-slate-500 opacity-0 hover:text-red-300 group-hover:opacity-100"
                :aria-label="t('astroAgent.deleteChat')"
                @click.stop="agent.deleteChat(c.id)"
              >
                ✕
              </button>
            </div>
          </li>
        </ul>
      </aside>

      <!-- Active conversation -->
      <section class="flex min-w-0 flex-1 flex-col gap-3">
        <div
          ref="transcriptEl"
          :class="[card, 'h-[60vh] space-y-3 overflow-y-auto']"
        >
          <p v-if="!agent.activeMessages.length" class="text-sm text-slate-400">
            {{ t("astroAgent.empty") }}
          </p>
          <div
            v-for="(m, i) in agent.activeMessages"
            :key="i"
            :class="rowClass(m.role)"
          >
            <div
              v-if="m.role === 'user'"
              class="flex max-w-[85%] flex-col items-end gap-1.5"
            >
              <div class="rounded-lg bg-brand-600 px-3 py-2 text-white">
                <div v-if="m.images?.length" class="mb-2 flex flex-wrap gap-2">
                  <img
                    v-for="(src, j) in m.images"
                    :key="j"
                    :src="src"
                    class="h-20 w-20 rounded object-cover"
                    alt=""
                  />
                </div>
                <p class="whitespace-pre-wrap break-words text-sm">
                  {{ m.text }}
                </p>
              </div>
              <ImageStatsPanel
                v-for="(mm, k) in m.measurements"
                :key="'stat' + k"
                :m="mm"
                class="w-full"
              />
            </div>
            <div v-else class="flex max-w-[85%] flex-col gap-2">
              <AgentActivity
                v-if="m.steps?.length"
                :steps="m.steps"
                :streaming="
                  i === agent.activeMessages.length - 1 && agent.streaming
                "
              />
              <div v-if="m.text" class="group relative">
                <div :class="bubbleClass('assistant')">
                  <MarkdownText :text="m.text" />
                </div>
                <button
                  type="button"
                  class="absolute right-1 top-1 rounded bg-slate-900/70 px-1.5 py-0.5 text-xs text-slate-300 opacity-0 transition-opacity hover:text-white group-hover:opacity-100"
                  @click="copyText(m.text, i)"
                >
                  {{
                    copiedIdx === i
                      ? t("astroAgent.copied")
                      : t("astroAgent.copy")
                  }}
                </button>
              </div>
            </div>
          </div>
          <AgentConfirm
            v-if="agent.pendingConfirm"
            :confirm="agent.pendingConfirm"
            @respond="onConfirm"
          />
          <div v-if="showThinking" class="animate-pulse text-sm text-slate-400">
            {{ t("astroAgent.thinking") }}
          </div>
        </div>

        <div
          v-if="error"
          class="rounded-md bg-red-900/40 px-3 py-2 text-sm text-red-200"
        >
          {{ error }}
        </div>

        <p
          v-if="agent.available && !selectedModel"
          class="text-sm text-amber-300"
        >
          {{ t("astroAgent.modelMissing") }}
        </p>

        <!-- Composer -->
        <div
          :class="[card, dragOver ? 'ring-2 ring-brand-500' : '']"
          @dragover.prevent="dragOver = true"
          @dragleave.prevent="dragOver = false"
          @drop.prevent="onDrop"
        >
          <div v-if="attachments.length" class="mb-2 flex flex-wrap gap-2">
            <div v-for="(src, i) in attachments" :key="i" class="relative">
              <img :src="src" class="h-16 w-16 rounded object-cover" alt="" />
              <button
                type="button"
                class="absolute -right-1 -top-1 flex h-5 w-5 items-center justify-center rounded-full bg-slate-900/80 text-xs text-white"
                :aria-label="t('astroAgent.removeImage')"
                @click="removeAttachment(i)"
              >
                ×
              </button>
            </div>
          </div>

          <textarea
            v-model="draft"
            :class="[input, 'min-h-[80px] resize-y']"
            :placeholder="t('astroAgent.placeholder')"
            @keydown="onKeydown"
          />

          <input
            ref="fileInput"
            type="file"
            accept="image/*"
            multiple
            class="hidden"
            @change="onFileChange"
          />
          <div class="mt-2 flex items-center gap-2">
            <button type="button" :class="btnGhost" @click="fileInput?.click()">
              {{ t("astroAgent.attach") }}
            </button>
            <span class="ml-auto hidden text-xs text-slate-400 sm:block">{{
              t("astroAgent.sendHint")
            }}</span>
            <button :class="btnPrimary" :disabled="!canSend" @click="send">
              {{ sending ? t("astroAgent.sending") : t("astroAgent.send") }}
            </button>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>
