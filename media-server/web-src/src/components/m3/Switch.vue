<script setup lang="ts">
defineProps<{ modelValue: boolean; disabled?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()
</script>

<template>
  <button
    class="sw"
    :class="{ on: modelValue }"
    role="switch"
    :aria-checked="modelValue"
    :disabled="disabled"
    type="button"
    @click="emit('update:modelValue', !modelValue)"
  >
    <i class="thumb" />
  </button>
</template>

<style scoped>
.sw {
  position: relative;
  flex: none;
  width: 52px;
  height: 32px;
  border-radius: var(--md-shape-full);
  border: 2px solid var(--md-sys-color-outline);
  background: var(--md-sys-color-surface-container-highest);
  transition: background-color var(--md-dur-medium) var(--md-ease-standard), border-color var(--md-dur-medium) var(--md-ease-standard);
}
.sw:disabled { opacity: .38; }
.thumb {
  position: absolute;
  top: 50%;
  left: 6px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--md-sys-color-outline);
  transform: translateY(-50%);
  transition: all var(--md-dur-medium) var(--md-ease-emphasized);
}
.sw:active .thumb { width: 28px; height: 28px; left: 2px; }
.sw.on { background: var(--md-sys-color-primary); border-color: var(--md-sys-color-primary); }
.sw.on .thumb { left: 24px; width: 24px; height: 24px; background: var(--md-sys-color-on-primary); }
.sw.on:active .thumb { left: 20px; width: 28px; height: 28px; }
</style>
