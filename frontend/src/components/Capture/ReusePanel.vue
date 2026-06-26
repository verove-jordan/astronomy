<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { card } from "@/constants/styles";
import { humanizeMs } from "@/utils/format";
import type { ReusePreview } from "@/types";

const props = defineProps<{ preview: ReusePreview | null }>();
const { t } = useI18n();

// Two-way: whether reuse is on, and which prior sessions are selected (session ids).
const enabled = defineModel<boolean>("enabled", { default: true });
const selected = defineModel<number[]>("selected", { default: () => [] });

const sessions = computed(() => props.preview?.reuse.sessions ?? []);
const hasPrior = computed(() => (props.preview?.reuse.prior_sessions ?? 0) > 0);

const selectedIntegrationMs = computed(() =>
  sessions.value
    .filter((s) => selected.value.includes(s.session_id))
    .reduce((sum, s) => sum + s.integration_ms, 0),
);

function toggle(id: number, on: boolean) {
  const set = new Set(selected.value);
  if (on) set.add(id);
  else set.delete(id);
  selected.value = [...set];
}
</script>

<template>
  <div v-if="hasPrior" :class="card">
    <div class="flex items-center justify-between gap-3">
      <div>
        <h3 class="font-semibold">{{ t("reuse.title") }}</h3>
        <p class="text-xs text-slate-500 dark:text-slate-400">
          {{
            t("reuse.subtitle", {
              object: preview?.object || "?",
              sessions: preview?.reuse.prior_sessions ?? 0,
            })
          }}
        </p>
      </div>
      <label class="flex items-center gap-2 text-sm">
        <input v-model="enabled" type="checkbox" class="accent-brand-500" />
        {{ t("reuse.enable") }}
      </label>
    </div>

    <p class="mt-2 text-sm text-brand-600 dark:text-brand-400">
      {{
        t("reuse.added", {
          frames: sessions
            .filter((s) => selected.includes(s.session_id))
            .reduce((n, s) => n + s.frames, 0),
          time: humanizeMs(selectedIntegrationMs),
        })
      }}
    </p>

    <ul v-show="enabled" class="mt-3 space-y-1">
      <li
        v-for="s in sessions"
        :key="s.session_id"
        class="flex items-center gap-3 rounded px-2 py-1 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
      >
        <input
          type="checkbox"
          class="accent-brand-500"
          :checked="selected.includes(s.session_id)"
          @change="
            toggle(s.session_id, ($event.target as HTMLInputElement).checked)
          "
        />
        <span class="font-mono text-xs text-slate-500"
          >#{{ s.session_id }}</span
        >
        <span>{{ t("reuse.frames", { n: s.frames }) }}</span>
        <span class="text-slate-400">·</span>
        <span>{{ humanizeMs(s.integration_ms) }}</span>
        <span class="ml-auto flex gap-1">
          <span
            v-for="f in s.filters"
            :key="f"
            class="rounded bg-slate-200 px-1.5 text-xs dark:bg-slate-700"
            >{{ f }}</span
          >
        </span>
      </li>
    </ul>
  </div>
</template>
