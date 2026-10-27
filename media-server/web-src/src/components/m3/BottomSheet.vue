<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps<{ modelValue: boolean; title?: string }>()
const emit = defineEmits<{ (e: 'update:modelValue', v: boolean): void }>()

const dragY = ref(0)
const dragging = ref(false)
let startY = 0
let startT = 0

function close() {
  emit('update:modelValue', false)
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}
watch(
  () => props.modelValue,
  (open) => {
    dragY.value = 0
    if (open) window.addEventListener('keydown', onKey)
    else window.removeEventListener('keydown', onKey)
  },
)
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

// 拖动手柄下拉：超过 120px 或快速下滑就关闭，否则弹回
function down(e: PointerEvent) {
  dragging.value = true
  startY = e.clientY
  startT = e.timeStamp
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}
function move(e: PointerEvent) {
  if (dragging.value) dragY.value = Math.max(0, e.clientY - startY)
}
function up(e: PointerEvent) {
  if (!dragging.value) return
  dragging.value = false
  const speed = dragY.value / Math.max(1, e.timeStamp - startT)
  if (dragY.value > 120 || speed > 0.6) close()
  else dragY.value = 0
}
</script>

<template>
  <Teleport to="body">
    <Transition name="sheet">
      <div v-if="modelValue" class="sheet-root">
        <div class="scrim" @click="close" />
        <section
          class="sheet"
          :class="{ dragging }"
          :style="{ transform: `translateY(${dragY}px)` }"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
        >
          <div class="handle-area" @pointerdown="down" @pointermove="move" @pointerup="up" @pointercancel="up">
            <i class="handle" />
          </div>
          <h2 v-if="title" class="t-title-l sheet-title">{{ title }}</h2>
          <div class="sheet-body"><slot /></div>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.sheet-root { position: fixed; inset: 0; z-index: 100; display: flex; align-items: flex-end; justify-content: center; }
.scrim { position: absolute; inset: 0; background: var(--md-sys-color-scrim); opacity: 0.32; }
.sheet {
  position: relative;
  width: 100%;
  max-width: 640px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  background: var(--md-sys-color-surface-container-low);
  color: var(--md-sys-color-on-surface);
  border-radius: 28px 28px 0 0;
  box-shadow: var(--md-elev-3);
  padding-bottom: var(--safe-bottom);
  transition: transform var(--md-dur-medium) var(--md-ease-emphasized);
}
.sheet.dragging { transition: none; }
.handle-area { display: flex; justify-content: center; padding: 12px 0 4px; touch-action: none; cursor: grab; }
.handle { width: 32px; height: 4px; border-radius: 2px; background: var(--md-sys-color-on-surface-variant); opacity: 0.4; }
.sheet-title { margin: 4px 24px 12px; }
.sheet-body { overflow-y: auto; padding: 0 24px 24px; }

/* 进出场：遮罩淡入淡出，面板从底部滑入 */
.sheet-enter-active .scrim, .sheet-leave-active .scrim { transition: opacity var(--md-dur-medium) linear; }
.sheet-enter-from .scrim, .sheet-leave-to .scrim { opacity: 0; }
.sheet-enter-active .sheet { transition: transform 400ms var(--md-ease-emphasized-decel); }
.sheet-leave-active .sheet { transition: transform 200ms var(--md-ease-emphasized-accel); }
.sheet-enter-from .sheet, .sheet-leave-to .sheet { transform: translateY(100%) !important; }
</style>
