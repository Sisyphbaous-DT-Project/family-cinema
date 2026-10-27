<script setup lang="ts">
import { computed } from 'vue'
import { ICONS } from './icons'

const props = withDefaults(defineProps<{ name: string; size?: number }>(), { size: 24 })
const d = computed(() => ICONS[props.name] ?? '')
</script>

<template>
  <svg class="m3-icon" :width="size" :height="size" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
    <path :d="d" />
  </svg>
</template>

<style scoped>
.m3-icon { flex: none; display: block; }
</style>
