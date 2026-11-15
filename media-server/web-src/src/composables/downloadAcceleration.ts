import { api } from '../api/client'
import { fmtSize } from '../api/local'
import type { Task } from '../api/manage'
import { toast } from './snackbar'

type AccelTask = Pick<Task, 'accelEnabled' | 'accelSupported' | 'accelReason' | 'accelSpeedKBs' | 'accelPhones' | 'accelConnections'>

export function accelText(t: AccelTask): string {
  if (t.accelSupported === false) return t.accelReason || '这个视频不支持分段'
  if (!t.accelEnabled) return ''
  const parts: string[] = []
  if ((t.accelSpeedKBs ?? 0) > 0) parts.push(`加速通道 ${fmtSize((t.accelSpeedKBs ?? 0) * 1024)}/s`)
  else parts.push('加速已开启')
  if ((t.accelPhones ?? 0) > 0) parts.push(`${t.accelPhones} 台手机`)
  if ((t.accelConnections ?? 0) > 0) parts.push(`${t.accelConnections} 路并行`)
  return `（${parts.join(' · ')}）`
}

export async function toggleDownloadAcceleration(t: AccelTask & { id: string }): Promise<boolean> {
  try {
    const enabled = !t.accelEnabled
    const r = await api.setDownloadAcceleration(t.id, enabled)
    if (!r.ok) throw new Error(r.error || '切换没成功')
    toast(enabled
      ? `下载加速已开启${r.phones ? `，发现 ${r.phones} 台手机节点` : '，暂由盒子分段；打开手机 App 解析助手后会自动加入'}`
      : '下载加速已关闭，已下载的部分会保留')
    return true
  } catch (e) {
    toast(e instanceof Error ? e.message : '切换下载加速失败')
    return false
  }
}
