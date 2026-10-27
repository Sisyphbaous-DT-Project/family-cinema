// 与后端 /api/anime/* 的 JSON 结构一一对应
export interface Images {
  large: string
  common: string
  medium: string
  small: string
  grid: string
}

export interface Card {
  id: number
  name: string
  name_cn: string
  date: string
  summary: string
  eps: number
  score: number
  rank: number
  images: Images
}

export interface CalendarDay {
  weekday: number // 1=周一 … 7=周日
  items: Card[]
}

export interface SearchResult {
  total: number
  offset: number
  items: Card[]
}

export interface Subject {
  id: number
  name: string
  name_cn: string
  summary: string
  date: string
  platform: string
  eps: number
  images: Images
  tags: { name: string; count: number }[]
  infobox: { key: string; value: string }[]
  rating: { rank: number; total: number; score: number }
}

export interface BgmEpisode {
  id: number
  sort: number
  name: string
  name_cn: string
  airdate: string
  duration: string
  desc: string
}

export interface RuleInfo {
  name: string
  version: string
  unsupported?: string
  adBlocker: boolean
}

export interface SourceItem {
  name: string
  src: string
}

export interface Episode {
  name: string
  url: string
}

export interface Road {
  name: string
  episodes: Episode[]
}

export interface ResolveResult {
  status: 'ok' | 'need-worker' | 'fail'
  kind?: 'hls' | 'mp4'
  playUrl?: string
  via?: 'static' | 'sniff'
  reason?: string
}
