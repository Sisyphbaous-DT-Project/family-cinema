<script setup lang="ts">
import { computed } from 'vue'
import { icons } from './icons'

const props = withDefaults(defineProps<{ name: keyof typeof icons; size?: number }>(), { size: 22 })
const path = computed(() => icons[props.name])
</script>

<template>
  <svg class="icon" :width="size" :height="size" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
    <path :d="path" />
  </svg>
</template>
