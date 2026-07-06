import { defineStore } from "pinia";
import { ref } from "vue";
import { apiGet, apiPost, apiPut, apiDelete, BASE } from "@/services/api";

// S3 connection manager: CRUD + test for encrypted S3 connections (credentials entered in the UI, stored
// encrypted server-side), and the object-browser operations (buckets/objects, upload, download, delete,
// create folder/bucket) over any connection. The secret access key is write-only — it's sent on save/test
// and never returned. The default connection also drives the pipeline (import/process/backup).

export interface S3Connection {
  id: number;
  name: string;
  endpoint: string;
  region: string;
  access_key_id: string;
  use_ssl: boolean;
  is_default: boolean;
  created_at: number;
  updated_at: number;
}

// ConnForm is the create/update payload; secret_access_key is blank to keep the stored one on edit.
export interface ConnForm {
  name: string;
  endpoint: string;
  region: string;
  access_key_id: string;
  secret_access_key: string;
  use_ssl: boolean;
  make_default?: boolean;
}

export interface S3ObjectEntry {
  key: string;
  name: string;
  size?: number;
  mod_time_ms?: number;
  is_dir: boolean;
}

export interface TestResult {
  ok: boolean;
  buckets?: string[];
  error?: string;
}

const enc = encodeURIComponent;

export const useS3ConnStore = defineStore("s3conn", () => {
  const connections = ref<S3Connection[]>([]);
  const loading = ref(false);
  const error = ref("");

  async function list(): Promise<void> {
    loading.value = true;
    error.value = "";
    try {
      const d = await apiGet<{ connections: S3Connection[] }>(
        "/api/s3/connections",
      );
      connections.value = d.connections || [];
    } catch (e) {
      error.value = (e as Error).message;
    } finally {
      loading.value = false;
    }
  }

  async function create(form: ConnForm): Promise<number> {
    const d = await apiPost<{ id: number }>("/api/s3/connections", form);
    await list();
    return d.id;
  }
  async function update(id: number, form: ConnForm): Promise<void> {
    await apiPut(`/api/s3/connections/${id}`, form);
    await list();
  }
  async function remove(id: number): Promise<void> {
    await apiDelete(`/api/s3/connections/${id}`);
    await list();
  }
  async function setDefault(id: number): Promise<void> {
    await apiPost(`/api/s3/connections/${id}/default`);
    await list();
  }
  // test tries UNSAVED credentials (the "Test connection" button in the form).
  function test(form: ConnForm): Promise<TestResult> {
    return apiPost<TestResult>("/api/s3/connections/test", form);
  }
  function testSaved(id: number): Promise<TestResult> {
    return apiPost<TestResult>(`/api/s3/connections/${id}/test`);
  }

  // --- object browser (over a connection id) ---
  async function buckets(conn: number): Promise<string[]> {
    const d = await apiGet<{ buckets: string[] }>(
      `/api/s3/manage/buckets?conn=${conn}`,
    );
    return d.buckets || [];
  }
  function createBucket(
    conn: number,
    name: string,
    region = "",
  ): Promise<unknown> {
    return apiPost(`/api/s3/manage/buckets?conn=${conn}`, { name, region });
  }
  function deleteBucket(
    conn: number,
    bucket: string,
    force = false,
  ): Promise<unknown> {
    return apiDelete(
      `/api/s3/manage/buckets?conn=${conn}&bucket=${enc(bucket)}${force ? "&force=1" : ""}`,
    );
  }
  async function objects(
    conn: number,
    bucket: string,
    prefix: string,
  ): Promise<S3ObjectEntry[]> {
    const d = await apiGet<{ objects: S3ObjectEntry[] }>(
      `/api/s3/manage/objects?conn=${conn}&bucket=${enc(bucket)}&prefix=${enc(prefix)}`,
    );
    return d.objects || [];
  }
  function createFolder(
    conn: number,
    bucket: string,
    key: string,
  ): Promise<unknown> {
    return apiPost(
      `/api/s3/manage/folder?conn=${conn}&bucket=${enc(bucket)}&key=${enc(key)}`,
    );
  }
  function deleteObject(
    conn: number,
    bucket: string,
    key: string,
  ): Promise<unknown> {
    return apiDelete(
      `/api/s3/manage/object?conn=${conn}&bucket=${enc(bucket)}&key=${enc(key)}`,
    );
  }
  function downloadUrl(conn: number, bucket: string, key: string): string {
    return `${BASE}/api/s3/manage/download?conn=${conn}&bucket=${enc(bucket)}&key=${enc(key)}`;
  }
  // upload streams the raw File as the request body (the backend keys it under prefix + filename).
  async function upload(
    conn: number,
    bucket: string,
    key: string,
    file: File,
  ): Promise<void> {
    const res = await fetch(
      `${BASE}/api/s3/manage/upload?conn=${conn}&bucket=${enc(bucket)}&key=${enc(key)}`,
      { method: "POST", body: file },
    );
    if (!res.ok) throw new Error(`upload failed (${res.status})`);
  }

  return {
    connections,
    loading,
    error,
    list,
    create,
    update,
    remove,
    setDefault,
    test,
    testSaved,
    buckets,
    createBucket,
    deleteBucket,
    objects,
    createFolder,
    deleteObject,
    downloadUrl,
    upload,
  };
});
