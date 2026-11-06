<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import Icon from '../Icon.vue'

const props = defineProps<{ open: boolean; title: string; confirm: string; icon?: 'info' | 'volume' | 'wifi' }>()
const emit = defineEmits<{ confirm: []; cancel: [] }>()
const dialog = ref<HTMLDialogElement>()
function update() {
  if (!dialog.value) return
  if (props.open && !dialog.value.open) dialog.value.showModal()
  else if (!props.open && dialog.value.open) dialog.value.close()
}
watch(() => props.open, update)
onMounted(update)
</script>

<template>
  <dialog ref="dialog" class="confirm-dialog" @cancel.prevent="emit('cancel')">
    <Icon :name="icon ?? 'info'" :size="28" />
    <h2>{{ title }}</h2>
    <div class="dialog-copy"><slot /></div>
    <div class="dialog-actions">
      <button class="button text" type="button" @click="emit('cancel')">暂不</button>
      <button class="button filled" type="button" @click="emit('confirm')">{{ confirm }}</button>
    </div>
  </dialog>
</template>
