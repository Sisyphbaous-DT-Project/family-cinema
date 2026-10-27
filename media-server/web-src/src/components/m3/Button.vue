<script setup lang="ts">
withDefaults(defineProps<{
  variant?: 'filled' | 'tonal' | 'outlined' | 'text'
  disabled?: boolean
  /** 小号按钮（列表行内操作用） */
  small?: boolean
  /** 危险操作（删除等），红色 */
  danger?: boolean
}>(), { variant: 'filled' })
</script>

<template>
  <button v-ripple class="m3-btn" :class="['is-' + variant, { small, danger }]" :disabled="disabled" type="button">
    <slot />
  </button>
</template>

<style scoped>
.m3-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  height: 40px;
  padding: 0 20px;
  border-radius: var(--md-shape-md); /* 圆角矩形（全站统一，不用两头圆的药丸形） */
  font-size: 14px;
  font-weight: 500;
  letter-spacing: 0.1px;
  white-space: nowrap;
  transition: box-shadow var(--md-dur-short) linear, background-color var(--md-dur-short) linear;
}
.is-filled { background: var(--md-sys-color-primary); color: var(--md-sys-color-on-primary); }
.is-filled:hover { box-shadow: var(--md-elev-1); }
.is-tonal { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
.is-outlined { border: 1px solid var(--md-sys-color-outline); color: var(--md-sys-color-primary); }
.is-text { color: var(--md-sys-color-primary); padding: 0 12px; }
.small { height: 34px; padding: 0 14px; font-size: 14px; border-radius: 10px; }
.is-text.small { padding: 0 10px; }
.danger.is-filled { background: var(--md-sys-color-error, #ba1a1a); color: var(--md-sys-color-on-error, #fff); }
.danger.is-text, .danger.is-outlined, .danger.is-tonal { color: var(--md-sys-color-error, #ba1a1a); }
</style>
