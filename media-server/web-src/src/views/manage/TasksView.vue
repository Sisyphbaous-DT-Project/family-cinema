<script setup lang="ts">
// 下载任务：列表 + 停止 + 清理记录。对应旧版 openTasks/refreshTasks。
import { clearTaskRecords, stopTask, type Task } from '../../api/manage'
import { fmtSize } from '../../api/local'
import { toast } from '../../composables/snackbar'
import { useServerTasks } from '../../composables/serverTasks'
import TopAppBar from '../../components/m3/TopAppBar.vue'
import Button from '../../components/m3/Button.vue'
import Icon from '../../components/m3/Icon.vue'

const { tasks, tasksError: error, refreshTasks: load } = useServerTasks()

const statusText = (t: Task) =>
  t.status === 'running'
    ? (t.progress || 0).toFixed(1) + '%'
    : t.status === 'done'
      ? '已完成'
      : t.status === 'error'
        ? '失败'
        : t.status === 'processing'
          ? t.stage || '整理中'
          : '已停止'

const speedText = (t: Task) =>
  t.speedKBs ? (t.speedKBs > 1024 ? (t.speedKBs / 1024).toFixed(2) + ' MB/s' : Math.round(t.speedKBs) + ' KB/s') : ''

async function stop(t: Task) {
  try {
    const d = await stopTask(t.id)
    // 后端 HTTP 200 也可能是 ok:false（任务已结束/不存在），不能一律报成功
    toast(d.ok ? '任务已停止' : '没能停止，任务可能已经结束')
    load()
  } catch {
    toast('停止失败')
  }
}

async function clearRecords() {
  try {
    const d = await clearTaskRecords()
    toast('已清理 ' + (d.cleared || 0) + ' 条任务记录')
    load()
  } catch {
    toast('清理失败')
  }
}
</script>

<template>
  <div class="page">
    <TopAppBar title="下载任务" back>
      <template #actions>
        <Button variant="text" small :disabled="!tasks.length" @click="clearRecords">清理记录</Button>
      </template>
    </TopAppBar>

    <div class="wrap">
      <div v-if="error" class="state">
        <Icon name="error" :size="40" />
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="load">再试一次</Button>
      </div>

      <div v-else-if="!tasks.length" class="state">
        <Icon name="download" :size="48" />
        <div class="t-title-m">没有下载任务</div>
      </div>

      <div v-for="t in tasks" :key="t.id" class="task">
        <div class="ttop">
          <span class="t-body-m clamp-1 grow">{{ t.name }}</span>
          <span class="t-label-m" :class="'st-' + t.status">{{ statusText(t) }}</span>
        </div>
        <div class="bar"><i :class="{ done: t.status === 'done' }" :style="{ width: Math.min(100, t.status === 'done' ? 100 : t.progress || 0) + '%' }" /></div>
        <p v-if="t.status === 'running' && t.stage" class="t-body-s muted stagetext">{{ t.stage }}</p>
        <div class="t-body-s muted facts">
          <span v-if="t.bytesTotal">{{ fmtSize(t.bytesDone) }} / {{ fmtSize(t.bytesTotal) }}</span>
          <span v-else-if="t.bytesDone">已下载 {{ fmtSize(t.bytesDone) }}</span>
          <span v-if="speedText(t)">{{ speedText(t) }}</span>
          <span v-if="t.peers">{{ t.peers }} 节点</span>
        </div>
        <p v-if="t.error" class="t-body-s err">{{ t.error }}</p>
        <p v-if="t.status === 'done' && t.targetDir" class="t-body-s muted clamp-1">{{ t.targetDir }}</p>
        <div v-if="t.status === 'running'" class="acts">
          <Button variant="text" small danger @click="stop(t)">停止任务</Button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wrap { padding: 8px 16px 24px; max-width: 720px; margin: 0 auto; display: flex; flex-direction: column; gap: 10px; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 72px 16px; text-align: center; color: var(--md-sys-color-on-surface-variant); }
.task { padding: 14px 16px; border-radius: var(--fc-radius); background: var(--fc-card); box-shadow: var(--fc-float); display: flex; flex-direction: column; gap: 8px; }
.ttop { display: flex; align-items: center; gap: 12px; }
.grow { flex: 1; min-width: 0; }
.bar { height: 4px; border-radius: 2px; background: var(--md-sys-color-surface-container-highest); overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--md-sys-color-primary); transition: width var(--md-dur-long) linear; }
.bar i.done { background: var(--md-sys-color-primary); }
.facts { display: flex; gap: 12px; }
.stagetext { margin: 0; }
.err { color: var(--md-sys-color-error, #ba1a1a); margin: 0; }
.st-error { color: var(--md-sys-color-error, #ba1a1a); }
.st-done { color: var(--md-sys-color-primary); }
.acts { display: flex; justify-content: flex-end; }
</style>
