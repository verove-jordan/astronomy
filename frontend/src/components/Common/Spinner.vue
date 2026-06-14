<template>
  <div role="status" class="inline-flex items-center gap-2 text-slate-500 dark:text-slate-400">
    <span
      class="h-4 w-4 rounded-full border-2 border-slate-300 border-t-brand-500 motion-safe:animate-spin dark:border-slate-600 dark:border-t-brand-400"
      aria-hidden="true"
    />
    <span class="sr-only">Loading</span>
    <slot />
  </div>
</template>
