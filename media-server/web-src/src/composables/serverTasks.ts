import { onBeforeUnmount, onMounted, ref } from 'vue'
import { fetchTasks, type Task } from '../api/manage'

// 所有页面共用一份任务状态：有下载在跑时每秒刷新，空闲时五秒看一次。
const tasks = ref<Task[]>([])
const tasksError = ref('')
let subscribers = 0
let timer: ReturnType<typeof setTimeout> | undefined
let busy = false

function schedule() {
  clearTimeout(timer)
  if (!subscribers) return
  const active = tasks.value.some((t) => t.status === 'running' || t.status === 'processing')
  timer = setTimeout(refreshTasks, active ? 1000 : 5000)
}

async function refreshTasks() {
  if (busy) return
  busy = true
  try {
    tasks.value = (await fetchTasks()) ?? []
    tasksError.value = ''
  } catch (e) {
    tasksError.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    busy = false
    schedule()
  }
}

export function useServerTasks() {
  onMounted(() => {
    subscribers++
    if (!timer) refreshTasks()
  })
  onBeforeUnmount(() => {
    subscribers = Math.max(0, subscribers - 1)
    if (!subscribers) {
      clearTimeout(timer)
      timer = undefined
    }
  })
  return { tasks, tasksError, refreshTasks }
}
