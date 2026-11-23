<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 24 })
</script>

<template>
  <svg
    class="brand-mark"
    :width="size"
    :height="size"
    viewBox="0 0 32 32"
    fill="none"
    aria-hidden="true"
  >
    <rect width="32" height="32" rx="9" fill="currentColor" />
    <path
      d="M16 6.2c.35 4.8 2.5 7.05 7.3 7.4-4.8.35-6.95 2.6-7.3 7.4-.35-4.8-2.5-7.05-7.3-7.4 4.8-.35 6.95-2.6 7.3-7.4Z"
      fill="#fff"
    />
    <path
      d="M23.1 19.6c.18 2.4 1.25 3.52 3.65 3.7-2.4.18-3.47 1.3-3.65 3.7-.18-2.4-1.25-3.52-3.65-3.7 2.4-.18 3.47-1.3 3.65-3.7Z"
      fill="#fff"
      fill-opacity=".62"
    />
  </svg>
</template>

<style scoped>
.brand-mark {
  display: block;
  flex: none;
  color: var(--accent);
}
</style>
