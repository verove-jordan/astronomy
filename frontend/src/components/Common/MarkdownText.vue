<script setup lang="ts">
import { computed } from "vue";
import { renderModelText } from "@/utils/richText";

const props = defineProps<{ text: string }>();

// renderModelText turns the reply into safe HTML: Markdown (bold/headings/lists/tables/code) plus
// pretty-printed, syntax-highlighted JSON for ```json fences and bare-JSON replies. See utils/richText.
const rendered = computed(() => renderModelText(props.text));
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -- markdown-it output is sanitized (html:false) -->
  <div class="astro-md break-words text-sm leading-relaxed" v-html="rendered" />
</template>

<style scoped>
.astro-md :deep(h1),
.astro-md :deep(h2),
.astro-md :deep(h3),
.astro-md :deep(h4) {
  font-weight: 600;
  line-height: 1.25;
  margin: 0.7em 0 0.3em;
}
.astro-md :deep(h1) {
  font-size: 1.15rem;
}
.astro-md :deep(h2) {
  font-size: 1.08rem;
}
.astro-md :deep(h3) {
  font-size: 1rem;
}
.astro-md :deep(h4) {
  font-size: 0.92rem;
}
.astro-md :deep(p) {
  margin: 0.4em 0;
}
.astro-md :deep(ul),
.astro-md :deep(ol) {
  margin: 0.4em 0;
  padding-left: 1.3rem;
}
.astro-md :deep(ul) {
  list-style: disc;
}
.astro-md :deep(ol) {
  list-style: decimal;
}
.astro-md :deep(li) {
  margin: 0.15em 0;
}
.astro-md :deep(code) {
  background: rgba(148, 163, 184, 0.22);
  padding: 0.05em 0.3em;
  border-radius: 0.25rem;
  font-size: 0.85em;
}
.astro-md :deep(pre) {
  background: rgba(2, 6, 23, 0.6);
  padding: 0.6rem 0.75rem;
  border-radius: 0.4rem;
  overflow-x: auto;
  margin: 0.5em 0;
}
.astro-md :deep(pre code) {
  background: transparent;
  padding: 0;
}
/* Highlighted JSON: a sky accent to mark structured data, plus a color per token type. */
.astro-md :deep(pre.astro-json) {
  border-left: 2px solid rgba(56, 189, 248, 0.5);
}
.astro-md :deep(.tok-key) {
  color: #7dd3fc;
}
.astro-md :deep(.tok-str) {
  color: #86efac;
}
.astro-md :deep(.tok-num) {
  color: #fcd34d;
}
.astro-md :deep(.tok-bool) {
  color: #c4b5fd;
}
.astro-md :deep(.tok-null) {
  color: #94a3b8;
  font-style: italic;
}
.astro-md :deep(a) {
  color: #93c5fd;
  text-decoration: underline;
}
.astro-md :deep(strong) {
  font-weight: 600;
}
.astro-md :deep(em) {
  font-style: italic;
}
.astro-md :deep(blockquote) {
  border-left: 3px solid rgba(148, 163, 184, 0.4);
  padding-left: 0.6rem;
  margin: 0.5em 0;
  color: #cbd5e1;
}
.astro-md :deep(hr) {
  border: 0;
  border-top: 1px solid rgba(148, 163, 184, 0.3);
  margin: 0.7em 0;
}
.astro-md :deep(table) {
  border-collapse: collapse;
  margin: 0.5em 0;
}
.astro-md :deep(th),
.astro-md :deep(td) {
  border: 1px solid rgba(148, 163, 184, 0.3);
  padding: 0.25rem 0.5rem;
}
.astro-md :deep(> :first-child) {
  margin-top: 0;
}
.astro-md :deep(> :last-child) {
  margin-bottom: 0;
}
</style>
