import { defineStore } from "pinia";
import { ref, computed } from "vue";
import { apiGet, apiPost, agentTurnEventsUrl } from "@/services/api";
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

// AgentOption is one choice the agent offers (a fix to apply, a folder to process…).
export interface AgentOption {
  id: string;
  label: string;
  detail?: string;
}

// AgentStep is one streamed step of an agent turn — a thought, a tool call (with its result merged in),
// a confirmation/choice request, an error, or the final answer. Mirrors the Go agent.Event.
export interface AgentStep {
  kind: "thinking" | "tool_call" | "tool_result" | "confirm" | "ask" | "error";
  step?: number;
  text?: string;
  tool?: string;
  args?: string;
  output?: string;
  is_error?: boolean;
  mutating?: boolean;
  call_id?: string;
  question?: string;
  options?: AgentOption[];
  preview?: string; // supervised pass: server file path of the rendered image (wrapped with fileUrl)
  resolved?: boolean; // confirm/ask: the user has answered
  answer?: string; // confirm/ask: what they chose (approve/decline or option label)
}

// A pending confirmation the agent is blocked on: the UI shows an approve/reject or choice card.
export interface PendingConfirm {
  turnId: string;
  callId: string;
  kind: "confirm" | "ask";
  tool?: string;
  args?: string;
  question?: string;
  options?: AgentOption[];
}

// One chat turn. Images are base64 data URLs (browser FileReader output) attached to user turns;
// measurements are the backend's objective stats for those images (user turns only); steps are the
// agent's tool-activity log accumulated while an assistant turn streams.
export interface AgentChatMessage {
  role: "user" | "assistant";
  text: string;
  images?: string[];
  measurements?: AssistMeasurement[];
  steps?: AgentStep[];
}

// A stored conversation: full message history (incl. images) so it can be continued at any time.
// turnId is set for a supervised-job conversation (a backend-spawned turn we watch rather than start);
// live is true only while its SSE stream is open this session (so its composer steers instead of chats).
export interface Conversation {
  id: string;
  title: string;
  model?: string;
  turnId?: string;
  live?: boolean;
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
      // A persisted `live` flag is stale after a reload — the SSE stream is gone. Reset it so a reopened
      // supervised conversation shows its transcript read-only rather than pretending to still steer.
      conversations.value.forEach((c) => (c.live = false));
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

  // --- a running agent turn (streamed over SSE) ---
  const streaming = ref(false);
  const pendingConfirm = ref<PendingConfirm | null>(null);

  // wireMessages strips client-only fields (steps/measurements) so the backend receives just the
  // conversation (role/text/images) it needs to seed the turn.
  function wireMessages(msgs: AgentChatMessage[]) {
    return msgs.map((m) => ({ role: m.role, text: m.text, images: m.images }));
  }

  // send runs one agent turn: it appends the user turn, starts the turn on the backend, then streams the
  // agent's steps (thoughts, tool calls, confirmations, final answer) onto a new assistant message. It
  // resolves when the turn finishes. Throws on the initial API error (the user turn is already saved).
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

    const body: Record<string, unknown> = {
      messages: wireMessages(conv.messages),
    };
    if (opts.model) body.model = opts.model;
    const data = await apiPost<{
      turn_id: string;
      measurements?: AssistMeasurement[];
    }>("/api/agent/chat", body);
    if (data.measurements && data.measurements.length) {
      conv.messages[userIdx].measurements = data.measurements;
    }

    const assistant: AgentChatMessage = {
      role: "assistant",
      text: "",
      steps: [],
    };
    conv.messages.push(assistant);
    conv.updatedAt = Date.now();
    persist();
    await streamTurn(data.turn_id, assistant);
    conv.updatedAt = Date.now();
    persist();
  }

  // streamTurn opens the turn's SSE stream and accumulates each step onto the assistant message
  // (merging a tool_result into its tool_call). Application is idempotent by "kind:step:call_id" so a
  // reconnect that replays the backlog never duplicates rows. Resolves on the terminal "done" event.
  function streamTurn(
    turnId: string,
    assistant: AgentChatMessage,
  ): Promise<void> {
    return new Promise((resolve) => {
      streaming.value = true;
      const src = new EventSource(agentTurnEventsUrl(turnId));
      const seen = new Set<string>();
      let tick = 0;
      const steps = () => (assistant.steps ??= []);
      // The raw SSE event carries the two terminal kinds ("done"/"final") on top of the display kinds.
      type AgentEvent = Omit<AgentStep, "kind"> & {
        kind: AgentStep["kind"] | "done" | "final";
      };
      src.onmessage = (ev: MessageEvent<string>) => {
        const e = JSON.parse(ev.data) as AgentEvent;
        if (e.kind === "done") {
          src.close();
          streaming.value = false;
          pendingConfirm.value = null;
          persist();
          resolve();
          return;
        }
        const sig = `${e.kind}:${e.step ?? ""}:${e.call_id ?? ""}`;
        if (seen.has(sig)) return;
        seen.add(sig);
        if (e.kind === "final") {
          assistant.text = e.text || assistant.text;
        } else if (e.kind === "tool_result") {
          const call = [...steps()]
            .reverse()
            .find(
              (s) =>
                s.kind === "tool_call" &&
                s.tool === e.tool &&
                s.output === undefined,
            );
          if (call) {
            call.output = e.output;
            call.is_error = e.is_error;
          } else steps().push(e as AgentStep);
        } else if (e.kind === "confirm" || e.kind === "ask") {
          steps().push(e as AgentStep);
          pendingConfirm.value = {
            turnId,
            callId: e.call_id ?? "",
            kind: e.kind,
            tool: e.tool,
            args: e.args,
            question: e.question,
            options: e.options,
          };
        } else {
          steps().push(e as AgentStep); // thinking / tool_call / error
        }
        if (++tick % 3 === 0) persist();
      };
      src.onerror = () => {
        // The browser auto-reconnects and replays the backlog; the `seen` set keeps it idempotent.
      };
    });
  }

  // respondConfirm answers the agent's pending confirmation/choice, marks the step resolved, and
  // unblocks the server-side loop.
  async function respondConfirm(
    approve: boolean,
    choice?: string,
  ): Promise<void> {
    const pc = pendingConfirm.value;
    if (!pc) return;
    const step = active.value?.messages
      .flatMap((m) => m.steps ?? [])
      .find((s) => s.call_id === pc.callId);
    if (step) {
      step.resolved = true;
      step.answer =
        pc.kind === "ask"
          ? (pc.options?.find((o) => o.id === choice)?.label ?? choice ?? "")
          : approve
            ? "approved"
            : "declined";
    }
    pendingConfirm.value = null;
    persist();
    await apiPost(`/api/agent/turns/${pc.turnId}/confirm`, {
      call_id: pc.callId,
      approve,
      choice,
    });
  }

  // conversationByTurn finds the conversation bound to a backend turn (a supervised finish), if any.
  function conversationByTurn(turnId: string): Conversation | null {
    return conversations.value.find((c) => c.turnId === turnId) ?? null;
  }

  // watchTurn attaches to a backend-spawned turn (a supervised finish) and streams it into a persisted
  // conversation, reusing the same step model + rendering as the chat — so the run reads as a live agent
  // turn (with per-pass previews) and lands in the history sidebar. The turn id is unique per run, so this
  // creates a fresh conversation; a second call while it is already streaming is a no-op.
  async function watchTurn(
    turnId: string,
    meta: { title?: string } = {},
  ): Promise<void> {
    let conv = conversationByTurn(turnId);
    if (conv?.live) return;
    if (!conv) {
      conv = {
        id: crypto.randomUUID(),
        title: meta.title || "Finish",
        turnId,
        live: true,
        createdAt: Date.now(),
        updatedAt: Date.now(),
        messages: [{ role: "assistant", text: "", steps: [] }],
      };
      conversations.value.push(conv);
    }
    conv.live = true;
    if (meta.title) conv.title = meta.title;
    let assistant = conv.messages[conv.messages.length - 1];
    if (!assistant || assistant.role !== "assistant") {
      assistant = { role: "assistant", text: "", steps: [] };
      conv.messages.push(assistant);
    }
    persist();
    try {
      await streamTurn(turnId, assistant);
    } finally {
      const c = conversationByTurn(turnId);
      if (c) {
        c.live = false;
        c.updatedAt = Date.now();
      }
      persist();
    }
  }

  // steerTurn sends a free-text nudge and/or a stop request to a live supervised turn, optimistically
  // showing the user's message in the conversation. The backend folds the text into the next pass's
  // critique (and a stop keeps the best pass so far).
  async function steerTurn(
    turnId: string,
    text: string,
    stop = false,
  ): Promise<void> {
    const trimmed = text.trim();
    if (!trimmed && !stop) return;
    const conv = conversationByTurn(turnId);
    if (conv) {
      conv.messages.push({
        role: "user",
        text: trimmed || "⏹ Stop & keep the best pass",
      });
      conv.updatedAt = Date.now();
      persist();
    }
    await apiPost(`/api/agent/turns/${turnId}/message`, {
      text: trimmed,
      stop,
    });
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
    streaming,
    pendingConfirm,
    newChat,
    selectChat,
    deleteChat,
    renameChat,
    send,
    respondConfirm,
    conversationByTurn,
    watchTurn,
    steerTurn,
  };
});
