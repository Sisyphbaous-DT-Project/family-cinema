<script setup lang="ts">
import Icon from './Icon.vue'
import Spinner from './Spinner.vue'

defineProps<{
  selected?: boolean
  loading?: boolean
  disabled?: boolean
  /** 右上小标记，如搜索到的条目数 */
  badge?: string | number
}>()
</script>

<template>
  <button v-ripple class="m3-chip" :class="{ 'is-selected': selected }" :disabled="disabled" type="button">
    <Spinner v-if="loading" :size="16" />
    <Icon v-else-if="selected" name="check" :size="18" />
    <span class="label"><slot /></span>
    <span v-if="badge !== undefined && badge !== ''" class="badge">{{ badge }}</span>
  </button>
</template>

<style scoped>
.m3-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  height: 32px;
  padding: 0 16px;
  border-radius: var(--md-shape-sm);
  border: 1px solid var(--md-sys-color-outline);
  color: var(--md-sys-color-on-surface-variant);
  font-size: 14px;
  font-weight: 500;
  letter-spacing: 0.1px;
  white-space: nowrap;
  flex: none;
  transition: background-color var(--md-dur-medium) var(--md-ease-standard), padding var(--md-dur-medium) var(--md-ease-emphasized);
}
.m3-chip.is-selected {
  background: var(--md-sys-color-secondary-container);
  color: var(--md-sys-color-on-secondary-container);
  border-color: transparent;
  padding-left: 8px;
}
.badge {
  font-size: 11px;
  padding: 0 6px;
  border-radius: var(--md-shape-full);
  background: var(--md-sys-color-surface-container-highest);
  color: var(--md-sys-color-on-surface-variant);
  line-height: 18px;
}
</style>
