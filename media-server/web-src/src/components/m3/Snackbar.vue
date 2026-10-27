<script setup lang="ts">
import { snackbar } from '../../composables/snackbar'
</script>

<template>
  <Transition name="snack">
    <div v-if="snackbar.show" :key="snackbar.id" class="snackbar t-body-m" role="status">{{ snackbar.text }}</div>
  </Transition>
</template>

<style scoped>
.snackbar {
  position: fixed;
  left: 50%;
  bottom: calc(var(--nav-space) + 4px);
  z-index: 200;
  max-width: min(560px, calc(100vw - 32px));
  padding: 14px 18px;
  border-radius: 14px;
  background: var(--md-sys-color-inverse-surface);
  color: var(--md-sys-color-inverse-on-surface);
  box-shadow: var(--md-elev-3);
  transform: translateX(-50%);
}
@media (min-width: 840px) { .snackbar { bottom: 24px; } }
.snack-enter-active { transition: opacity 150ms linear, transform 300ms var(--md-ease-emphasized-decel); }
.snack-leave-active { transition: opacity 100ms linear; }
.snack-enter-from { opacity: 0; transform: translate(-50%, 16px); }
.snack-leave-to { opacity: 0; }
</style>
