<script setup lang="ts">
withDefaults(defineProps<{ size?: number }>(), { size: 40 })
</script>

<template>
  <svg class="m3-spinner" :width="size" :height="size" viewBox="0 0 48 48" role="progressbar" aria-label="加载中">
    <circle class="arc" cx="24" cy="24" r="18" fill="none" stroke="currentColor" stroke-width="5" stroke-linecap="round" />
  </svg>
</template>

<style scoped>
.m3-spinner { flex: none; color: var(--md-sys-color-primary); animation: rot 1.4s linear infinite; }
.arc { stroke-dasharray: 20 94; animation: dash 1.4s ease-in-out infinite; transform-origin: center; }
@keyframes rot { to { transform: rotate(360deg); } }
@keyframes dash {
  0% { stroke-dasharray: 4 110; stroke-dashoffset: 0; }
  50% { stroke-dasharray: 70 44; stroke-dashoffset: -20; }
  100% { stroke-dasharray: 4 110; stroke-dashoffset: -112; }
}
</style>
