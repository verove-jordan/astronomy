<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import GenericTable, {
  type Column,
} from "@/components/Common/GenericTable.vue";
import Pill from "@/components/Common/Pill.vue";
import { card } from "@/constants/styles";
import { useMosaicStore } from "@/stores/mosaic";
import { useNowTicker } from "@/composables/useNowTicker";
import { altAzAt } from "@/utils/altaz";
import { decToDMS, raToHMS } from "@/utils/sexagesimal";

// The planner's tile list: capture order, pointing in sexagesimal, live altitude, transit +
// meridian side. Row selection is two-way with the Aladin footprint overlay.
const { t } = useI18n();
const store = useMosaicStore();
const now = useNowTicker();

interface TileRow extends Record<string, unknown> {
  index: number;
  order: number;
  id: string;
  ra: string;
  raDeg: number;
  dec: string;
  decDeg: number;
  alt: number;
  transit: string;
  meridian: string;
  status: string;
}

const rows = computed<TileRow[]>(() => {
  const p = store.preview;
  if (!p) return [];
  const lat = p.query.lat;
  const lon = p.query.lon;
  const statuses = store.activePlan?.tile_status ?? {};
  return p.tiles.map((tile) => ({
    index: tile.index,
    order: tile.order,
    id: `${tile.folder} · r${tile.row}c${tile.col}`,
    ra: raToHMS(tile.ra_deg),
    raDeg: tile.ra_deg,
    dec: decToDMS(tile.dec_deg),
    decDeg: tile.dec_deg,
    alt: Math.round(
      altAzAt(tile.ra_deg, tile.dec_deg, lat, lon, now.value).altDeg,
    ),
    transit: new Date(tile.transit_utc_ms).toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
    }),
    meridian: tile.meridian_side,
    status: statuses[String(tile.index)] ?? "pending",
  }));
});

const columns = computed<Column<TileRow>[]>(() => [
  { key: "order", label: t("mosaic.table.order"), sortable: true },
  { key: "id", label: t("mosaic.table.tile"), searchable: true },
  { key: "ra", label: t("mosaic.table.ra"), sortable: true },
  { key: "dec", label: t("mosaic.table.dec"), sortable: true },
  { key: "alt", label: t("mosaic.table.alt"), sortable: true, align: "right" },
  { key: "transit", label: t("mosaic.table.transit"), sortable: true },
  { key: "status", label: t("mosaic.table.status") },
  { key: "actions", label: "" },
]);

// Shooting a tile: select it, then hand it to the Capture page in a NEW TAB with the mount already
// on its way. A new tab rather than a navigation because the planner is the map you keep referring
// back to — losing it mid-session to go and shoot one panel is exactly the friction this removes.
function captureTile(row: TileRow, event?: Event) {
  event?.stopPropagation();
  store.selectedTileIndex = row.index;
  const plan = store.activePlan;
  if (!plan) {
    captureError.value = t("mosaic.table.savePlanFirst");
    return;
  }
  captureError.value = "";
  const url = `/capture?plan=${plan.id}&tile=${row.index}&goto=1`;
  window.open(url, `capture-${plan.id}-${row.index}`);
}

const captureError = ref("");

// Typed by the exposed surface, not InstanceType — GenericTable is generic (house pattern, see
// DarkSkyFinder).
const tableRef = ref<{ scrollToKey: (k: string | number) => void } | null>(
  null,
);
function scrollToTile(index: number) {
  tableRef.value?.scrollToKey(index);
}
defineExpose({ scrollToTile });

const statusPillClass: Record<string, string> = {
  pending: "bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300",
  captured:
    "bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300",
  skipped:
    "bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300",
};
</script>

<template>
  <div v-if="rows.length" :class="card">
    <h2 class="mb-2 text-sm font-semibold text-slate-700 dark:text-slate-200">
      {{ t("mosaic.table.title", { count: rows.length }) }}
    </h2>
    <GenericTable
      ref="tableRef"
      :columns="columns"
      :rows="rows"
      :row-key="(r) => r.index"
      :row-class="
        (r) =>
          r.index === store.selectedTileIndex
            ? 'bg-brand-50/60 dark:bg-brand-900/20'
            : ''
      "
      max-height="22rem"
      @row-click="(r) => (store.selectedTileIndex = r.index)"
    >
      <template #cell-order="{ row }">
        <span
          class="inline-flex h-6 w-6 items-center justify-center rounded-full bg-slate-200 text-xs font-bold text-slate-700 dark:bg-slate-700 dark:text-slate-100"
          >{{ row.order }}</span
        >
      </template>
      <template #cell-ra="{ row }">
        <span class="font-mono text-xs">{{ row.ra }}</span>
      </template>
      <template #cell-dec="{ row }">
        <span class="font-mono text-xs">{{ row.dec }}</span>
      </template>
      <template #cell-alt="{ row }">{{ row.alt }}°</template>
      <template #cell-transit="{ row }">
        <span class="text-xs"
          >{{ row.transit }}
          <Pill
            :color-class="
              row.meridian === 'west'
                ? 'bg-sky-100 text-sky-800 dark:bg-sky-900/40 dark:text-sky-300'
                : 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300'
            "
            >{{ t(`mosaic.table.${row.meridian}`) }}</Pill
          ></span
        >
      </template>
      <template #cell-status="{ row }">
        <Pill :color-class="statusPillClass[row.status]">{{
          t(`mosaic.status.${row.status}`)
        }}</Pill>
      </template>
      <template #cell-actions="{ row }">
        <button
          class="whitespace-nowrap rounded-md border border-brand-500/50 px-2 py-0.5 text-xs text-brand-600 hover:bg-brand-50 dark:text-brand-300 dark:hover:bg-brand-900/20"
          :title="t('mosaic.table.captureHint')"
          @click="captureTile(row, $event)"
        >
          {{ t("mosaic.table.capture") }}
        </button>
      </template>
    </GenericTable>
    <p v-if="captureError" class="mt-1 text-xs text-danger-500">
      {{ captureError }}
    </p>
  </div>
</template>
