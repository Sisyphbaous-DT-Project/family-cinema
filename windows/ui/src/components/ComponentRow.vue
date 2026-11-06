<script setup lang="ts">
import Icon from '../Icon.vue'
import type { icons } from '../icons'

defineProps<{
  title: string
  description: string
  icon: keyof typeof icons
  modelValue?: boolean
  required?: boolean
  disabled?: boolean
  warning?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>

<template>
  <label class="component-row" :class="{ unavailable: disabled && !required }">
    <Icon :name="icon" :size="23" />
    <span class="row-copy">
      <span class="row-title">{{ title }}</span>
      <span class="row-description" :class="{ 'warning-copy': warning }">{{ description }}</span>
    </span>
    <span v-if="required" class="required-label"><Icon name="check" :size="17" />已包含</span>
    <span v-else class="checkbox">
      <input type="checkbox" :checked="modelValue" :disabled="disabled" :aria-label="title"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)" />
      <Icon name="check" :size="17" />
    </span>
  </label>
</template>
