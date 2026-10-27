<script setup lang="ts">
import Icon from './Icon.vue'

defineProps<{ options: { value: string; label: string }[]; modelValue: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <div class="seg" role="radiogroup">
    <button
      v-for="o in options"
      :key="o.value"
      v-ripple
      class="seg-item t-label-l"
      :class="{ on: o.value === modelValue }"
      role="radio"
      :aria-checked="o.value === modelValue"
      type="button"
      @click="emit('update:modelValue', o.value)"
    >
      <Icon v-if="o.value === modelValue" name="check" :size="18" />
      {{ o.label }}
    </button>
  </div>
</template>

<style scoped>
.seg { display: inline-flex; border: 1px solid var(--md-sys-color-outline); border-radius: var(--md-shape-md); overflow: hidden; }
.seg-item {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 40px;
  padding: 0 16px;
  color: var(--md-sys-color-on-surface);
  transition: background-color var(--md-dur-medium) var(--md-ease-standard);
}
.seg-item + .seg-item { border-left: 1px solid var(--md-sys-color-outline); }
.seg-item.on { background: var(--md-sys-color-secondary-container); color: var(--md-sys-color-on-secondary-container); }
</style>
