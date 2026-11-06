<script setup lang="ts">
import Icon from '../Icon.vue'

defineProps<{ label: string; modelValue: string; disabled?: boolean; error?: string }>()
const emit = defineEmits<{
  'update:modelValue': [value: string]
  browse: []
  blur: []
}>()
</script>

<template>
  <div class="directory-field">
    <label class="outlined-field" :class="{ invalid: error, disabled }">
      <span class="field-label">{{ label }}</span>
      <input :value="modelValue" :disabled="disabled" :aria-invalid="!!error"
        :aria-label="label" spellcheck="false" autocomplete="off"
        @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
        @blur="emit('blur')" />
      <button class="icon-button" type="button" :disabled="disabled" :title="'选择' + label"
        :aria-label="'选择' + label" @click.prevent="emit('browse')">
        <Icon name="folder" :size="21" />
      </button>
    </label>
    <p v-if="error" class="field-error">{{ error }}</p>
  </div>
</template>
