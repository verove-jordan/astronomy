<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useJobStream } from "@/composables/useJobStream";
import TransferProgress from "@/components/Common/TransferProgress.vue";

// The explorer's live S3-move strip: subscribes to the move job's SSE stream and renders TransferProgress.
// The parent mounts it keyed by jobId, so each new move remounts a fresh stream. It emits `done` when the
// job finishes (or fails) so the parent can refresh the browser and hide the strip.
const props = defineProps<{
  jobId: number;
  count: number; // number of items being moved (for the heading)
  startedAtMs: number; // client-captured enqueue time (elapsed = time since you clicked Move)
}>();
const emit = defineEmits<{ done: [] }>();

const { t } = useI18n();
const { progress, step, done, bytesDone, bytesTotal, bytesPerSec } =
  useJobStream(props.jobId, () => emit("done"), true);

const title = computed(() => t("storage.move.moving", { n: props.count }));
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
