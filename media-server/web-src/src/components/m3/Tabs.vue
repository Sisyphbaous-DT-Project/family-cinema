<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'

const props = defineProps<{ tabs: string[]; modelValue: number }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: number): void }>()

const btns = ref<HTMLElement[]>([])
const indicator = ref({ left: 0, width: 0 })

// 指示条跟着当前标签的文字宽度滑动
function measure() {
  const el = btns.value[props.modelValue]
  if (!el) return
  const label = el.querySelector('span') as HTMLElement
  const w = Math.max(24, label.offsetWidth)
  indicator.value = { left: el.offsetLeft + (el.offsetWidth - w) / 2, width: w }
}
onMounted(() => nextTick(measure))
watch(() => [props.modelValue, props.tabs.length], () => nextTick(measure))
</script>

<template>
  <div class="tabs" role="tablist">
    <button
      v-for="(t, i) in tabs"
      :key="t"
      :ref="(el) => { if (el) btns[i] = el as HTMLElement }"
      v-ripple
      class="tab t-title-s"
      :class="{ active: i === modelValue }"
      role="tab"
      type="button"
      @click="emit('update:modelValue', i)"
    >
      <span>{{ t }}</span>
    </button>
    <i class="indicator" :style="{ transform: `translateX(${indicator.left}px)`, width: indicator.width + 'px' }" />
  </div>
</template>

<style scoped>
.tabs {
  position: relative;
  display: flex;
  background: var(--md-sys-color-surface);
  box-shadow: inset 0 -1px var(--md-sys-color-surface-container-highest);
}
.tab {
  flex: 1;
  height: 48px;
  color: var(--md-sys-color-on-surface-variant);
  transition: color var(--md-dur-medium) var(--md-ease-standard);
}
.tab.active { color: var(--md-sys-color-primary); }
.indicator {
  position: absolute;
  left: 0;
  bottom: 0;
  height: 3px;
  border-radius: 3px 3px 0 0;
  background: var(--md-sys-color-primary);
  transition: transform 520ms var(--ease-spring), width 520ms var(--ease-spring);
}
</style>
