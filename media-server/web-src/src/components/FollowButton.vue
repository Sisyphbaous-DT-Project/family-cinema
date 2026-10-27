<script setup lang="ts">
import { computed, ref } from 'vue'
import { isFollowed, toggleFollow, useFollows } from '../composables/follows'
import Button from './m3/Button.vue'
import Icon from './m3/Icon.vue'

// 追番按钮：没追是实心"追番"，追了变浅色"已追番"，再点取消
const props = defineProps<{
  subject: { id: number; name_cn: string; name: string; images?: { common?: string; large?: string } }
  /** 正在看的片源（播放页传），追番后以这个站查更新 */
  source?: { rule: string; src: string } | null
  small?: boolean
}>()

useFollows()
const on = computed(() => isFollowed(props.subject.id))
const busy = ref(false)

async function click() {
  if (busy.value) return
  busy.value = true
  await toggleFollow(props.subject, props.source ?? undefined)
  busy.value = false
}
</script>

<template>
  <Button :variant="on ? 'tonal' : 'filled'" :small="small" :disabled="busy" @click="click">
    <Icon :name="on ? 'favorite' : 'favoriteBorder'" :size="small ? 18 : 20" />
    {{ on ? '已追番' : '追番' }}
  </Button>
</template>
