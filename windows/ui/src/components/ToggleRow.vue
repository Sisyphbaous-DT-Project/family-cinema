<script setup lang="ts">
import Icon from '../Icon.vue'
import type { icons } from '../icons'

defineProps<{ title: string; description?: string; icon: keyof typeof icons; modelValue: boolean; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>

<template>
  <label class="toggle-row" :class="{ disabled }">
    <Icon :name="icon" :size="21" />
    <span class="row-copy">
      <span class="row-title">{{ title }}</span>
      <span v-if="description" class="row-description">{{ description }}</span>
    </span>
    <span class="switch">
      <input type="checkbox" role="switch" :checked="modelValue" :disabled="disabled" :aria-label="title"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)" />
      <span class="switch-track"><span class="switch-thumb"><Icon name="check" :size="14" /></span></span>
    </span>
  </label>
</template>
