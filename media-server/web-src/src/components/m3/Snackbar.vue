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
.snack-enter-active { transition: opacity 180ms linear, transform 520ms var(--ease-spring); }
.snack-leave-active { transition: opacity 160ms ease-in, transform 200ms var(--ease-exit); }
.snack-enter-from { opacity: 0; transform: translate(-50%, 20px) scale(0.9); }
.snack-leave-to { opacity: 0; transform: translate(-50%, 8px) scale(0.96); }
</style>
