import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost } from "@/services/api";
import { idbGet, idbSet } from "@/utils/idb";

// Objective per-image stats the backend measured (fed to the model as ground truth, and shown in the
// stats panel under the image). Keys are snake_case to match the Go JSON.
export interface AssistMeasurement {
  background: number;
  median_rgb: [number, number, number];
  green_cast: number;
  black_clip: [number, number, number];
  white_clip: [number, number, number];
  gradient_pct: number;
  trail: boolean;
  trail_span: number;
}

// One chat turn. Images are base64 data URLs (browser FileReader output) attached to user turns;
// measurements are the backend's objective stats for those images (user turns only).
export interface AgentChatMessage {
  role: "user" | "assistant";
  text: string;
  images?: string[];
  measurements?: AssistMeasurement[];
}

// A stored conversation: full message history (incl. images) so it can be continued at any time.
export interface Conversation {
  id: string;
  title: string;
  model?: string;
  createdAt: number;
  updatedAt: number;
  messages: AgentChatMessage[];
}

interface AgentStatus {
  running: boolean;
  model: string;
  models: string[];
}

interface SendOptions {
  model?: string;
  maxTokens?: number;
  temperature?: number;
}

const STORAGE_KEY = "conversations";

// titleFrom derives a short conversation label from the first user message.
function titleFrom(text: string): string {
  const t = text.trim().replace(/\s+/g, " ");
  if (!t) return "";
  return t.length > 48 ? t.slice(0, 48) + "…" : t;
}

// The agent store tracks whether the local vision model server is up (polled app-wide to gate the
// AstroAgent nav link) and owns the locally-persisted conversations. The backend owns the model
// connection, so the frontend never talks to the model port directly.
export const useAgentStore = defineStore("agent", () => {
  // --- availability (polled app-wide) ---
  const available = ref(false); // model server reachable
  const model = ref(""); // configured default model id ("" → the user must pick one)
  const models = ref<string[]>([]); // ids the server advertises (for the picker)
  const checked = ref(false); // a status poll has returned at least once

  async function refreshStatus(): Promise<void> {
    try {
      const s = await apiGet<AgentStatus>("/api/agent/status");
      available.value = s.running;
      model.value = s.model || "";
      models.value = s.models || [];
    } catch {
      available.value = false;
      models.value = [];
    } finally {
      checked.value = true;
    }
  }

  // --- conversations (persisted in IndexedDB so they survive reloads and can be continued) ---
  const conversations = ref<Conversation[]>([]);
  const activeId = ref<string | null>(null);
  const loaded = ref(false);

  const orderedConversations = computed(() =>
    [...conversations.value].sort((a, b) => b.updatedAt - a.updatedAt),
  );
  const active = computed(
    () => conversations.value.find((c) => c.id === activeId.value) ?? null,
  );
  const activeMessages = computed(() => active.value?.messages ?? []);

  let persistTimer: ReturnType<typeof setTimeout> | null = null;
  function persist(): void {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => {
      // Plain-clone to drop Vue reactivity proxies before the structured clone into IndexedDB.
      void idbSet(STORAGE_KEY, JSON.parse(JSON.stringify(conversations.value)));
    }, 300);
  }

  async function load(): Promise<void> {
    try {
      const saved = await idbGet<Conversation[]>(STORAGE_KEY);
      conversations.value = Array.isArray(saved) ? saved : [];
      activeId.value = orderedConversations.value[0]?.id ?? null; // open the most recent, or a new chat
    } catch {
      conversations.value = [];
    } finally {
      loaded.value = true;
    }
  }
  void load();

  function newChat(): void {
    activeId.value = null; // a fresh, unsaved chat; the record is created on the first send
  }
  function selectChat(id: string): void {
    activeId.value = id;
  }
  function deleteChat(id: string): void {
    conversations.value = conversations.value.filter((c) => c.id !== id);
    if (activeId.value === id)
      activeId.value = orderedConversations.value[0]?.id ?? null;
    persist();
  }
  function renameChat(id: string, title: string): void {
    const c = conversations.value.find((x) => x.id === id);
    if (c && title.trim()) {
      c.title = title.trim();
      persist();
    }
  }

  // send appends the user turn to the active conversation (creating one on the first send), calls the
  // model with the full history, then appends the reply. Throws on API error — the user turn is already
  // saved, so it can be retried — and the caller surfaces the message.
  async function send(
    text: string,
    images: string[],
    opts: SendOptions = {},
  ): Promise<void> {
    let conv = active.value;
    if (!conv) {
      conv = {
        id: crypto.randomUUID(),
        title: titleFrom(text),
        model: opts.model,
        createdAt: Date.now(),
        updatedAt: Date.now(),
        messages: [],
      };
      conversations.value.push(conv);
      activeId.value = conv.id;
    }
    if (!conv.title) conv.title = titleFrom(text);
    if (opts.model) conv.model = opts.model;
    conv.messages.push({
      role: "user",
      text,
      images: images.length ? images : undefined,
    });
    const userIdx = conv.messages.length - 1;
    conv.updatedAt = Date.now();
    persist();

    const body: Record<string, unknown> = { messages: conv.messages };
    if (opts.model) body.model = opts.model;
    if (opts.maxTokens) body.max_tokens = opts.maxTokens;
    if (opts.temperature !== undefined) body.temperature = opts.temperature;
    const data = await apiPost<{
      reply: string;
      measurements?: AssistMeasurement[];
    }>("/api/agent/chat", body);

    // Attach the backend's objective stats to the user turn (for the stats panel), then the reply.
    if (data.measurements && data.measurements.length) {
      conv.messages[userIdx].measurements = data.measurements;
    }
    conv.messages.push({ role: "assistant", text: data.reply });
    conv.updatedAt = Date.now();
    persist();
  }

  return {
    available,
    model,
    models,
    checked,
    refreshStatus,
    conversations,
    orderedConversations,
    active,
    activeMessages,
    activeId,
    loaded,
    newChat,
    selectChat,
    deleteChat,
    renameChat,
    send,
  };
});
