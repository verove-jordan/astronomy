// Browser-only app state — the part of AstroStack that lives ONLY in the browser and is therefore invisible
// to a server-side backup: every astrostack.* localStorage entry (favorites, equipment setups, saved sky/
// event queries, map layers, GoTo/polar prefs, the S3 selection, section toggles…) plus `locale`, and the
// AstroAgent conversations (kept in IndexedDB because they hold base64 images too big for localStorage).
// exportAppState gathers it into one JSON blob for the backup; importAppState writes it back on restore.
// Without this, a "backup everything" would silently lose all of it.

import { idbGet, idbSet } from "@/utils/idb";

export interface AppState {
  version: number;
  localStorage: Record<string, string>;
  conversations?: unknown; // AstroAgent chats (IndexedDB astroagent/kv/"conversations")
}

const LS_PREFIX = "astrostack.";
const EXTRA_KEYS = ["locale"]; // non-namespaced but user-meaningful
const CHAT_KEY = "conversations";

// exportAppState collects every persisted browser key (prefix-based, so new astrostack.* keys are captured
// automatically) plus the AI-chat store, into a single serializable snapshot.
export async function exportAppState(): Promise<AppState> {
  const ls: Record<string, string> = {};
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i);
    if (!key) continue;
    if (key.startsWith(LS_PREFIX) || EXTRA_KEYS.includes(key)) {
      const v = localStorage.getItem(key);
      if (v !== null) ls[key] = v;
    }
  }
  let conversations: unknown;
  try {
    conversations = await idbGet(CHAT_KEY);
  } catch {
    conversations = undefined;
  }
  return { version: 1, localStorage: ls, conversations };
}

// importAppState writes a snapshot back into the browser. localStorage entries are set individually so a
// single bad/oversized value can't abort the whole restore; the AI chats go back to IndexedDB.
export async function importAppState(state: AppState): Promise<void> {
  if (state.localStorage) {
    for (const [k, v] of Object.entries(state.localStorage)) {
      try {
        localStorage.setItem(k, v);
      } catch {
        // quota or a read-only key — skip it, keep restoring the rest
      }
    }
  }
  if (state.conversations !== undefined) {
    try {
      await idbSet(CHAT_KEY, state.conversations);
    } catch {
      // IndexedDB unavailable — chats stay as they were
    }
  }
}
