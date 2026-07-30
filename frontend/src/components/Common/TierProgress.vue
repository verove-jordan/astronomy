<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useJobStream } from "@/composables/useJobStream";
import TransferProgress from "@/components/Common/TransferProgress.vue";

// The explorer's live storage-class-change strip: subscribes to the tier job's SSE stream and renders
// TransferProgress. Keyed by jobId so each new tier op remounts a fresh stream. Emits `done` when the job
// finishes (or fails) so the parent can refresh the browser and hide the strip. A thaw job stays "running"
// (parked) for a long time — the step text reads "thawing from Glacier…" while it polls.
const props = defineProps<{
  jobId: number;
  count: number; // number of items being retiered (for the heading)
  verb: string; // "archive" | "restore" | "retier" — selects the heading
  startedAtMs: number;
}>();
const emit = defineEmits<{ done: [] }>();

const { t } = useI18n();
const { progress, step, done, bytesDone, bytesTotal, bytesPerSec } =
  useJobStream(props.jobId, () => emit("done"), true);

const title = computed(() =>
  t(`storage.tier.progress.${props.verb}`, { n: props.count }),
);
</script>

<template>
  <TransferProgress
    :title="title"
    :progress="progress"
    :step="step"
    :bytes-done="bytesDone"
    :bytes-total="bytesTotal"
    :bytes-per-sec="bytesPerSec"
    :started-at-ms="startedAtMs"
    :running="!done"
  />
</template>
