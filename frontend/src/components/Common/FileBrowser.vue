<script setup lang="ts">
import { ref, reactive, computed, watch, nextTick } from "vue";
import { useI18n } from "vue-i18n";
import Breadcrumb from "@/components/Common/Breadcrumb.vue";
import Spinner from "@/components/Common/Spinner.vue";
import IconFolder from "@/components/Icons/IconFolder.vue";
import IconFile from "@/components/Icons/IconFile.vue";
import IconChevronRight from "@/components/Icons/IconChevronRight.vue";
import IconArrowUp from "@/components/Icons/IconArrowUp.vue";
import IconDownload from "@/components/Icons/IconDownload.vue";
import IconCloud from "@/components/Icons/IconCloud.vue";
import IconX from "@/components/Icons/IconX.vue";
import { usePathBreadcrumb } from "@/composables/usePathBreadcrumb";
import { btnGhost, btnPrimary, entrySelected, input } from "@/constants/styles";
import { PROCESSED_SINGLE } from "@/constants/colors";
import type { BrowseEntry, ProcessedFolder } from "@/types";

const props = withDefaults(
  defineProps<{
    path: string;
    root: string;
    entries: BrowseEntry[];
    loading: boolean;
    selected?: BrowseEntry[]; // folders checked across locations (drives checkboxes + pills)
    multiSelect?: boolean; // false → single-folder picker (no checkboxes/pills), e.g. live stacking
    error?: string;
    // fetchChildren lists a directory's subfolders so the columns to the LEFT of the active folder can
    // fill in (the active/deepest column is the `entries` prop). Without it the browser is single-column.
    fetchChildren?: (path: string) => Promise<BrowseEntry[]>;
    // processed maps a folder path → its past-processing info (a coloured dot; folders processed
    // together share a colour). Omitted by the single-folder picker (live stacking).
    processed?: Map<string, ProcessedFolder>;
    // s3Enabled turns on the S3 presence badges + per-selection transfer actions (Import view only).
    s3Enabled?: boolean;
    // sourceFilter scopes the browsable/selectable folders to one source (the Import Local/S3 tabs):
    // "local" hides cloud-only folders; "s3" shows only folders present on S3. Undefined = show all.
    sourceFilter?: "local" | "s3";
    // downloading: an S3-sourced selection is being pulled to local before inspect (drives the button).
    downloading?: boolean;
    // busyLabel: overrides the busy-button label (e.g. "Inspecting…" once the download finishes).
    busyLabel?: string;
    // actionLabel overrides the primary button's label entirely (e.g. the Storage drives browser's
    // "Copy N folders to S3") — the default labels are Import-specific inspect wording.
    actionLabel?: string;
    // manage: object-manager mode (the Storage explorer) — files are selectable too, each row exposes a
    // #row-actions slot, and rows can be dragged onto a folder to emit a "move". Off (default) = picker.
    manage?: boolean;
  }>(),
  { selected: () => [], multiSelect: true },
);

const emit = defineEmits<{
  (e: "navigate", path: string): void;
  (e: "inspect", paths: string[]): void;
  (e: "toggle", entry: BrowseEntry): void;
  (e: "clearSelection"): void;
  (e: "transfer", op: "upload" | "sync" | "download" | "removeLocal"): void;
  (e: "move", payload: { src: string; dst: string; srcIsDir: boolean }): void;
}>();

// selectable: which rows get a checkbox. Folders always (the picker); files too in manage mode (so the
// Storage explorer can multi-select files for a move).
function selectable(e: BrowseEntry): boolean {
  return props.multiSelect && (e.is_dir || props.manage === true);
}

// Drag-and-drop move (manage mode only): a row is dragged onto a folder to relocate it there. dragSrc is the
// dragged path; dragOver highlights the hovered drop-target folder. The actual move is the parent's job (it
// rewrites the S3 object + the ledger), triggered by the "move" emit.
const dragSrc = ref<{ path: string; isDir: boolean } | null>(null);
const dragOver = ref<string | null>(null);
function onDragStart(ev: DragEvent, entry: BrowseEntry) {
  if (!props.manage) return;
  dragSrc.value = { path: entry.path, isDir: entry.is_dir };
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = "move";
    ev.dataTransfer.setData("text/plain", entry.path); // some browsers won't start a drag without data
  }
}
function onDragEnd() {
  dragSrc.value = null;
  dragOver.value = null;
}
function onDragOver(ev: DragEvent, entry: BrowseEntry) {
  if (
    !props.manage ||
    !entry.is_dir ||
    !dragSrc.value ||
    dragSrc.value.path === entry.path
  )
    return;
  ev.preventDefault(); // required so the row becomes a valid drop target
  dragOver.value = entry.path;
}
function onDrop(entry: BrowseEntry) {
  if (!props.manage || !entry.is_dir || !dragSrc.value) return;
  if (dragSrc.value.path !== entry.path)
    emit("move", {
      src: dragSrc.value.path,
      dst: entry.path,
      srcIsDir: dragSrc.value.isDir,
    });
  onDragEnd();
}

const { t } = useI18n();

function isSelected(p: string): boolean {
  return props.selected.some((s) => s.path === p);
}

// filterBySource scopes a column's entries to the active source tab, but always keeps the folder the user
// has drilled into (keepPath) so the navigation path never disappears (an ancestor may be local-only).
function filterBySource(
  entries: BrowseEntry[],
  keepPath: string,
): BrowseEntry[] {
  const f = props.sourceFilter;
  if (!f) return entries;
  return entries.filter((e) => {
    if (e.path === keepPath) return true;
    if (f === "local") return !e.is_dir || e.local !== false; // local-only + synced (+ files)
    return e.is_dir && e.remote === true; // "s3": only folders present on S3
  });
}

// hasRemoteOnly: the selection includes a folder that lives only on S3 → it must download before inspect.
const hasRemoteOnly = computed(() =>
  props.selected.some((e) => e.remote && !e.local),
);

// inspectLabel adapts the primary button: downloading state, "Download & inspect" when the selection has
// S3-only folders, else the normal inspect/use-folder labels.
const inspectLabel = computed(() => {
  if (props.downloading) return props.busyLabel || t("import.downloadingBtn");
  if (props.actionLabel) return props.actionLabel;
  if (props.multiSelect && props.selected.length) {
    return hasRemoteOnly.value
      ? t("import.downloadInspect", { n: props.selected.length })
      : t("import.inspectN", { n: props.selected.length });
  }
  return t("import.useFolder");
});

// Past-processing annotation for a folder (a dot; siblings of one processing share a colour).
function proc(path: string): ProcessedFolder | undefined {
  return props.processed?.get(path.toLowerCase()); // keys are lower-cased (case-insensitive FS match)
}
function processedTitle(path: string): string {
  const info = proc(path);
  if (!info) return "";
  const obj = info.object ? ` (${info.object})` : "";
  return (
    (info.groupSize
      ? t("import.processedGroup", { n: info.groupSize })
      : t("import.processed")) + obj
  );
}

// Hover tooltip showing a file's full name instantly. The native `title` is too slow, and a CSS tooltip
// would be clipped by the columns' overflow — so it's teleported to <body> and positioned by hand.
const tip = ref<string | null>(null);
const tipPos = ref<{ left: string; top: string }>({ left: "0px", top: "0px" });
const tipEl = ref<HTMLElement | null>(null);

function showTip(e: MouseEvent, text: string) {
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  tip.value = text;
  tipPos.value = { left: `${r.left}px`, top: `${r.bottom + 6}px` };
  nextTick(() => placeTip(r)); // refine once measured: clamp to the viewport, flip above if needed
}
function placeTip(r: DOMRect) {
  const el = tipEl.value;
  if (!el) return;
  const pad = 8;
  let left = r.left;
  let top = r.bottom + 6;
  if (left + el.offsetWidth > window.innerWidth - pad)
    left = Math.max(pad, window.innerWidth - el.offsetWidth - pad);
  if (top + el.offsetHeight > window.innerHeight - pad)
    top = r.top - el.offsetHeight - 6;
  tipPos.value = { left: `${left}px`, top: `${top}px` };
}
function hideTip() {
  tip.value = null;
}

// The path from the root down to the active folder. Each crumb is a directory whose child folders form
// one column — exactly the macOS Finder column (Miller) layout.
const chain = usePathBreadcrumb(
  () => props.path,
  () => props.root,
);

// Children per directory. The active (deepest) directory is seeded from the `entries` prop, already
// fetched by the parent; ancestor columns are filled lazily via fetchChildren and cached.
const childrenCache = reactive(new Map<string, BrowseEntry[]>());
const colLoading = reactive(new Set<string>());

watch(
  () => [props.path, props.entries] as const,
  ([p, e]) => {
    // Seed the active directory's children from the parent-provided entries — including the S3
    // bucket root (p===""), whose entries are already loaded, so its column doesn't flash empty and
    // refetch while descending.
    childrenCache.set(p, e);
  },
  { immediate: true },
);

watch(
  chain,
  (crumbs) => {
    const fetchChildren = props.fetchChildren;
    if (!fetchChildren) return;
    for (const c of crumbs) {
      if (childrenCache.has(c.path) || colLoading.has(c.path)) continue;
      colLoading.add(c.path);
      fetchChildren(c.path)
        .then((entries) => childrenCache.set(c.path, entries))
        .catch(() => childrenCache.set(c.path, []))
        .finally(() => colLoading.delete(c.path));
    }
  },
  { immediate: true },
);

interface Column {
  dir: string;
  entries: BrowseEntry[];
  loading: boolean;
  active: string; // the child the user drilled into (highlighted), if any
}

const columns = computed<Column[]>(() => {
  const crumbs = chain.value;
  // No breadcrumb (e.g. the S3 tab rooted at "" at its top level): render the current entries as one
  // column so the listing still shows. Navigating into a folder gives a non-empty path → a real chain.
  if (!crumbs.length) {
    return [
      {
        dir: props.path,
        entries: filterBySource(props.entries, ""),
        loading: props.loading,
        active: "",
      },
    ];
  }
  if (!props.fetchChildren) {
    const leaf = crumbs[crumbs.length - 1];
    return leaf
      ? [
          {
            dir: leaf.path,
            entries: filterBySource(props.entries, ""),
            loading: props.loading,
            active: "",
          },
        ]
      : [];
  }
  return crumbs.map((c, i) => ({
    dir: c.path,
    entries: filterBySource(
      childrenCache.get(c.path) ?? [],
      crumbs[i + 1]?.path ?? "",
    ),
    loading:
      i === crumbs.length - 1
        ? props.loading && !childrenCache.has(c.path)
        : colLoading.has(c.path) && !childrenCache.has(c.path),
    active: crumbs[i + 1]?.path ?? "",
  }));
});

// Keep the newest (rightmost) column in view as the user drills deeper.
const colsEl = ref<HTMLElement | null>(null);
watch(
  () => chain.value.length,
  () =>
    nextTick(() => {
      if (colsEl.value) colsEl.value.scrollLeft = colsEl.value.scrollWidth;
    }),
);

// Primary action: inspect the checked selection, or — when nothing is checked — the active folder,
// preserving the original single-folder workflow.
function inspectAction() {
  if (props.multiSelect && props.selected.length) {
    emit(
      "inspect",
      props.selected.map((s) => s.path),
    );
  } else if (props.path) {
    emit("inspect", [props.path]);
  }
}

// Double-click is a single-folder quick-inspect, used by the single-select picker (live stacking).
function onRowDblclick(entry: BrowseEntry) {
  if (!props.multiSelect) emit("inspect", [entry.path]);
}

function goPath(e: Event) {
  emit("navigate", (e.target as HTMLInputElement).value.trim());
}
function up() {
  const p = (props.path || "").replace(/\/+$/, "");
  const r = (props.root || "").replace(/\/+$/, "");
  if (!p || p === r) return; // already at the root (empty S3 bucket root, or the clamped local root)
  // A slashless single segment (S3 one level below the bucket root, e.g. "M51") goes to the root "";
  // an absolute/nested path strips its last segment. Never climb above the configured root.
  const parent = p.includes("/") ? p.replace(/\/[^/]+$/, "") : "";
  emit("navigate", r && parent.length < r.length ? r : parent);
}
</script>

<template>
  <div class="space-y-3">
    <!-- address bar -->
    <div class="flex flex-wrap items-end gap-2">
      <div class="min-w-0 grow">
        <label class="mb-1 block text-xs font-medium text-slate-500">{{
          t("common.path")
        }}</label>
        <input
          :value="path"
          :class="input"
          type="text"
          spellcheck="false"
          data-demo="browse-path"
          @keyup.enter="goPath"
        />
      </div>
      <button :class="btnGhost" :disabled="loading" @click="up">
        <IconArrowUp /> {{ t("common.up") }}
      </button>
    </div>

    <Breadcrumb
      v-if="path"
      :path="path"
      :root="root"
      @navigate="(p) => emit('navigate', p)"
    />

    <!-- selection pills: every checked folder, removable; survives navigation -->
    <div
      v-if="multiSelect && selected.length"
      class="flex flex-wrap items-center gap-2"
    >
      <span class="text-xs font-medium text-slate-500">{{
        t("import.selectedCount", { n: selected.length })
      }}</span>
      <span v-for="s in selected" :key="s.path" :class="entrySelected">
        <!-- Cloud marker on folders that live only on S3 — these download before inspect. -->
        <IconCloud
          v-if="s.remote && !s.local"
          class="h-3 w-3 shrink-0 text-sky-500"
          :title="t('s3.badge.cloudOnly')"
        />
        <!-- Click the name to reveal that folder in the columns (it may live in another location). -->
        <button
          type="button"
          class="max-w-[12rem] cursor-pointer truncate hover:underline"
          :title="t('import.revealFolder', { path: s.path })"
          @click="emit('navigate', s.path)"
        >
          {{ s.name }}
        </button>
        <button
          class="-mr-0.5 ml-0.5 rounded-sm text-brand-500 hover:text-brand-700 dark:text-brand-300 dark:hover:text-brand-100"
          :aria-label="t('common.remove')"
          @click="emit('toggle', s)"
        >
          <IconX class="h-3.5 w-3.5" />
        </button>
      </span>
      <button
        :class="btnGhost"
        class="!px-2 !py-1 !text-xs"
        @click="emit('clearSelection')"
      >
        {{ t("common.clear") }}
      </button>

      <!-- S3 transfer actions for the selected folders (each becomes a job with a progress bar). -->
      <span
        v-if="s3Enabled"
        class="ml-auto inline-flex items-center gap-1 border-l border-slate-200 pl-2 dark:border-slate-700"
      >
        <button
          :class="btnGhost"
          class="inline-flex items-center gap-1 !px-2 !py-1 !text-xs"
          :title="t('s3.action.syncHint')"
          @click="emit('transfer', 'sync')"
        >
          <IconArrowUp class="h-3 w-3" /> {{ t("s3.action.sync") }}
        </button>
        <button
          :class="btnGhost"
          class="inline-flex items-center gap-1 !px-2 !py-1 !text-xs"
          :title="t('s3.action.downloadHint')"
          @click="emit('transfer', 'download')"
        >
          <IconDownload class="h-3 w-3" /> {{ t("s3.action.download") }}
        </button>
        <button
          :class="btnGhost"
          class="inline-flex items-center gap-1 !px-2 !py-1 !text-xs text-danger"
          :title="t('s3.action.removeLocalHint')"
          @click="emit('transfer', 'removeLocal')"
        >
          <IconX class="h-3 w-3" /> {{ t("s3.action.removeLocal") }}
        </button>
      </span>
    </div>

    <!-- primary action, kept right above the columns (next to the selection) so it's where the eye lands
         after checking folders — not stranded up in the address bar. Hidden in manage mode (no inspect). -->
    <div
      v-if="!manage && (path || (multiSelect && selected.length))"
      class="flex justify-end"
    >
      <button
        :class="btnPrimary"
        :disabled="loading || downloading || (!path && !selected.length)"
        data-demo="browse-inspect"
        @click="inspectAction"
      >
        {{ inspectLabel }}
      </button>
    </div>

    <!-- macOS-style cascading columns: each directory's subfolders sit in their own column; clicking a
         folder reveals its children in the next column to the right. -->
    <div
      ref="colsEl"
      class="flex min-h-[6rem] overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700"
    >
      <div
        v-if="loading && !columns.length"
        class="flex w-full items-center justify-center py-8"
        role="status"
      >
        <Spinner>{{ t("common.loading") }}</Spinner>
      </div>
      <div
        v-for="(col, ci) in columns"
        :key="col.dir"
        class="flex max-h-72 w-56 shrink-0 flex-col overflow-y-auto"
        :class="
          ci < columns.length - 1
            ? 'border-r border-slate-200 dark:border-slate-700'
            : ''
        "
      >
        <div
          v-if="col.loading"
          class="flex grow items-center justify-center py-6"
          role="status"
        >
          <Spinner />
        </div>
        <ul
          v-else-if="col.entries.length"
          class="divide-y divide-slate-100 dark:divide-slate-800"
        >
          <li
            v-for="e in col.entries"
            :key="e.path"
            class="flex items-center"
            :draggable="manage || undefined"
            :class="[
              e.path === col.active
                ? 'bg-brand-100 dark:bg-brand-900/30'
                : isSelected(e.path)
                  ? 'bg-brand-50 dark:bg-brand-900/15'
                  : '',
              manage && dragOver === e.path
                ? 'ring-2 ring-inset ring-brand-400'
                : '',
            ]"
            @dragstart="onDragStart($event, e)"
            @dragend="onDragEnd"
            @dragover="onDragOver($event, e)"
            @drop="onDrop(e)"
          >
            <!-- selection checkbox (folders always; files too in manage mode); other rows get a spacer -->
            <label
              v-if="selectable(e)"
              class="flex shrink-0 cursor-pointer items-center py-2 pl-2 pr-1"
              :title="t('import.selectFolder')"
            >
              <input
                type="checkbox"
                class="h-4 w-4 accent-brand-500"
                :checked="isSelected(e.path)"
                :aria-label="e.name"
                @change="emit('toggle', e)"
              />
            </label>
            <span
              v-else-if="multiSelect"
              class="w-7 shrink-0"
              aria-hidden="true"
            />

            <!-- folder: navigable / selectable -->
            <button
              v-if="e.is_dir"
              class="flex min-w-0 grow items-center gap-1.5 px-2 py-2 text-left text-sm transition-colors hover:bg-slate-50 dark:hover:bg-slate-800/60"
              :class="
                e.path === col.active
                  ? 'font-medium text-brand-700 dark:text-brand-200'
                  : 'text-slate-700 dark:text-slate-200'
              "
              @click="emit('navigate', e.path)"
              @dblclick="onRowDblclick(e)"
            >
              <IconFolder class="shrink-0 text-brand-500 dark:text-brand-300" />
              <span
                class="min-w-0 grow truncate"
                :class="
                  s3Enabled && e.remote && !e.local ? 'italic opacity-70' : ''
                "
                >{{ e.name }}</span
              >
              <!-- S3 presence: cloud = on S3; emerald when also local (synced), sky when only on S3. -->
              <IconCloud
                v-if="s3Enabled && e.remote"
                class="h-3.5 w-3.5 shrink-0"
                :class="e.local ? 'text-emerald-500' : 'text-sky-500'"
                :title="
                  e.local ? t('s3.badge.synced') : t('s3.badge.cloudOnly')
                "
              />
              <span
                v-if="proc(e.path)"
                class="h-2.5 w-2.5 shrink-0 rounded-full ring-1 ring-black/10 dark:ring-white/20"
                :style="{
                  backgroundColor: proc(e.path)?.groupColor || PROCESSED_SINGLE,
                }"
                :title="processedTitle(e.path)"
              />
              <IconChevronRight class="shrink-0 text-slate-400" />
            </button>
            <!-- file: context only in picker mode (greyed); a first-class row in manage mode. Hover reveals
                 the full name immediately -->
            <div
              v-else
              class="flex min-w-0 grow items-center gap-1.5 px-2 py-2 text-sm"
              :class="
                manage
                  ? 'text-slate-700 dark:text-slate-200'
                  : 'text-slate-400 dark:text-slate-500'
              "
              @mouseenter="showTip($event, e.name)"
              @mouseleave="hideTip"
            >
              <IconFile class="shrink-0" />
              <span class="min-w-0 grow truncate">{{ e.name }}</span>
            </div>

            <!-- per-row management actions (download / delete / move…), manage mode only -->
            <span
              v-if="manage"
              class="flex shrink-0 items-center gap-0.5 pr-1"
              @click.stop
            >
              <slot name="rowActions" :entry="e" />
            </span>
          </li>
        </ul>
        <p v-else class="px-3 py-6 text-center text-xs text-slate-400">
          {{ t("import.noFolders") }}
        </p>
      </div>
    </div>

    <p v-if="error" class="text-sm text-danger">{{ error }}</p>

    <!-- full-name tooltip: teleported so the columns' overflow can't clip it; appears instantly on hover -->
    <Teleport to="body">
      <div
        v-if="tip"
        ref="tipEl"
        class="pointer-events-none fixed z-[100] whitespace-nowrap rounded-md border border-slate-200 bg-white px-2 py-1 text-xs text-slate-700 shadow-lg dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
        :style="tipPos"
      >
        {{ tip }}
      </div>
    </Teleport>
  </div>
</template>
