<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(defineProps<{ icon: string; label?: string; size?: number; active?: boolean }>(), { size: 24 })
</script>

<template>
  <button v-ripple class="m3-iconbtn" :class="{ 'is-active': active }" type="button" :aria-label="label" :title="label">
    <Icon :name="icon" :size="size" />
  </button>
</template>

<style scoped>
.m3-iconbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border-radius: 50%;
  color: var(--md-sys-color-on-surface-variant);
  flex: none;
}
.is-active { color: var(--md-sys-color-primary); background: var(--md-sys-color-primary-container); }
</style>
