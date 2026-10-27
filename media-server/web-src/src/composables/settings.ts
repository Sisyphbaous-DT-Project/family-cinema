import { reactive } from 'vue'
import { readJSON, writeJSON } from './storage'

const KEY = 'fc.settings'

interface Settings {
  /** 被用户关闭的播放源规则名 */
  disabledRules: string[]
}

export const settings = reactive<Settings>({ disabledRules: [], ...readJSON<Partial<Settings>>(KEY, {}) })

function save() {
  writeJSON(KEY, { disabledRules: settings.disabledRules })
}

export const isRuleEnabled = (name: string) => !settings.disabledRules.includes(name)

export function setRuleEnabled(name: string, on: boolean) {
  const rest = settings.disabledRules.filter((n) => n !== name)
  settings.disabledRules = on ? rest : [...rest, name]
  save()
}
