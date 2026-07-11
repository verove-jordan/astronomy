// Minimal IndexedDB key/value helper (no dependency). Used to persist AstroAgent conversations, which
// can hold base64 images far larger than localStorage's ~5 MB quota. Client-only (this is an SPA).

const DB_NAME = "astroagent";
const STORE = "kv";

let dbPromise: Promise<IDBDatabase> | null = null;

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function db(): Promise<IDBDatabase> {
  return (dbPromise ??= openDB());
}

export async function idbGet<T>(key: string): Promise<T | undefined> {
  const conn = await db();
  return new Promise<T | undefined>((resolve, reject) => {
    const req = conn.transaction(STORE, "readonly").objectStore(STORE).get(key);
    req.onsuccess = () => resolve(req.result as T | undefined);
    req.onerror = () => reject(req.error);
  });
}

export async function idbSet(key: string, value: unknown): Promise<void> {
  const conn = await db();
  return new Promise<void>((resolve, reject) => {
    const tx = conn.transaction(STORE, "readwrite");
    tx.objectStore(STORE).put(value, key);
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}
