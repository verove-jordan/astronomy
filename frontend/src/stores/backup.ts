import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost, BASE } from "@/services/api";
import { useS3Store } from "@/stores/s3";
import {
  exportAppState,
  importAppState,
  type AppState,
} from "@/utils/appstate";

// Backup-everything: snapshot the precious local state (Postgres db, calibration library, LP atlas) plus
// the browser-only app state (favorites/setups/prefs + AI chats) to S3, and restore it. The server pieces
// run as jobs (progress in Tasks); the browser app state is gathered/applied here, since only the browser
// can read localStorage + the AI-chat IndexedDB. S3 credentials stay in the backend env.

export type BackupComponent = "db" | "library" | "atlas" | "appstate";

export interface BackupManifest {
  stamp: string;
  stamp_ms: number;
  components: string[];
  library_dir?: string;
  atlas_dir?: string;
}

export const useBackupStore = defineStore("backup", () => {
  const backups = ref<BackupManifest[]>([]);
  const loading = ref(false);
  const error = ref("");
  const s3 = useS3Store();

  function query(): string {
    return `bucket=${encodeURIComponent(s3.bucket)}&prefix=${encodeURIComponent(s3.prefix)}`;
  }

  async function list(): Promise<void> {
    if (!s3.active) {
      backups.value = [];
      return;
    }
    loading.value = true;
    error.value = "";
    try {
      const data = await apiGet<{ backups: BackupManifest[] }>(
        `/api/backup?${query()}`,
      );
      backups.value = data.backups || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  // backup enqueues a snapshot job. The browser app state (localStorage + AI chats) is gathered here — only
  // the browser can read it — and posted in the body; the server stores it as appstate.json in the backup.
  async function backup(components: BackupComponent[]): Promise<number> {
    let appstate = "";
    if (components.includes("appstate")) {
      appstate = JSON.stringify(await exportAppState());
    }
    const data = await apiPost<{ id: number }>("/api/backup", {
      bucket: s3.bucket,
      prefix: s3.prefix,
      components,
      appstate,
    });
    return data.id;
  }

  // restore enqueues the backend restore (db/library/atlas) and, when appstate is selected, re-imports the
  // browser state here (localStorage + the AI-chat IndexedDB can't be written server-side). Returns the
  // backend job id, or null when only appstate was restored (nothing to run server-side).
  async function restore(
    stamp: string,
    components: BackupComponent[],
  ): Promise<number | null> {
    const serverComps = components.filter((c) => c !== "appstate");
    let id: number | null = null;
    if (serverComps.length) {
      const data = await apiPost<{ id: number }>("/api/backup/restore", {
        bucket: s3.bucket,
        prefix: s3.prefix,
        stamp,
        components: serverComps,
      });
      id = data.id;
    }
    if (components.includes("appstate")) {
      await restoreAppState(stamp);
    }
    return id;
  }

  async function restoreAppState(stamp: string): Promise<void> {
    const res = await fetch(
      `${BASE}/api/backup/appstate?${query()}&stamp=${encodeURIComponent(stamp)}`,
    );
    if (!res.ok) throw new Error("app-state fetch failed");
    const state = (await res.json()) as AppState;
    await importAppState(state);
  }

  return { backups, loading, error, list, backup, restore };
});
