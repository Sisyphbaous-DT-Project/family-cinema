<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, titleOf } from '../api/client'
import { peekSubject } from '../api/bangumi'
import { coverHint, holdTransition } from '../composables/motion'
import { useExpand } from '../composables/useExpand'
import type { Subject } from '../api/types'
import TopAppBar from '../components/m3/TopAppBar.vue'
import Chip from '../components/m3/Chip.vue'
import Button from '../components/m3/Button.vue'
import Icon from '../components/m3/Icon.vue'
import FollowButton from '../components/FollowButton.vue'
import { useAirtime } from '../composables/airtime'

const route = useRoute()
const id = Number(route.params.id)
// 刚看过的直接用现成数据；没有的话先拿列表卡片上的封面和片名把海报画出来（换页时海报直接落在这里）
const peeked = peekSubject(id)
const hint = coverHint(id)
const subject = ref<Subject | null>(peeked)
const loading = ref(!peeked)
const error = ref('')
const { line: airLine, cn: airCn } = useAirtime(id, subject)
// 简介：收起时只露 5 行，展开/收起时高度平滑变化
const summaryEl = ref<HTMLElement | null>(null)
const { expanded, toggle: toggleSummary, overflowing } = useExpand(summaryEl, 5)

async function load() {
  loading.value = !subject.value
  error.value = ''
  try {
    subject.value = await api.subject(id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}
const loaded = load()
if (!peeked && !hint) holdTransition(loaded)

const title = computed(() => (subject.value ? titleOf(subject.value) : hint?.title ?? ''))
const cover = computed(() => subject.value?.images.common || subject.value?.images.large || hint?.img || '')
const imageState = reactive({ poster: 'loading', backdrop: 'loading' })
watch(cover, () => {
  imageState.poster = 'loading'
  imageState.backdrop = 'loading'
}, { flush: 'sync' })
function updateImageState(layer: 'poster' | 'backdrop', event: Event) {
  const image = event.currentTarget as HTMLImageElement
  if (image.getAttribute('src') !== cover.value) return
  imageState[layer] = event.type === 'load' && image.naturalWidth > 0 ? 'loaded' : 'error'
}
// 只展示几项最有用的制作信息
const KEYS = ['话数', '放送开始', '导演', '原作', '系列构成', '动画制作', '制作', '播放电视台']
const info = computed(() => {
  const all = subject.value?.infobox ?? []
  return KEYS.map((k) => all.find((x) => x.key === k)).filter((x): x is { key: string; value: string } => !!x)
})
</script>

<template>
  <div class="detail">
    <div class="backdrop" aria-hidden="true">
      <img
        v-if="cover"
        :key="cover"
        :src="cover"
        alt=""
        :class="{ show: imageState.backdrop === 'loaded' }"
        @load="updateImageState('backdrop', $event)"
        @error="updateImageState('backdrop', $event)"
      />
    </div>
    <div class="page detail-page">
      <TopAppBar class="detail-appbar" :title="title" back :fade="false" />

      <div v-if="loading && !hint" class="pad">
        <div class="top">
          <div class="skeleton poster-sk" />
          <div class="lines"><div class="skeleton l1" /><div class="skeleton l2" /></div>
        </div>
      </div>

      <div v-else-if="error && !subject" class="state">
        <div class="muted">{{ error }}</div>
        <Button variant="tonal" @click="load">再试一次</Button>
      </div>

      <template v-else-if="subject || hint">
        <div class="hero">
          <div class="top pad">
            <div class="poster" :data-hero="`subject:${id}|play:${id}`">
              <div v-if="cover && imageState.poster === 'loading'" class="skeleton fill" />
              <img
                v-if="cover"
                :key="cover"
                :src="cover"
                :alt="title"
                :class="{ show: imageState.poster === 'loaded' }"
                @load="updateImageState('poster', $event)"
                @error="updateImageState('poster', $event)"
              />
            </div>
            <div v-if="!subject" class="head lines">
              <h2 class="t-headline-s clamp-2">{{ title }}</h2>
              <div class="skeleton l2" />
              <div class="skeleton l3" />
            </div>
            <div v-else class="head reveal-soft">
              <h2 class="t-headline-s clamp-2">{{ title }}</h2>
              <div v-if="subject.name_cn && subject.name !== subject.name_cn" class="t-body-m muted clamp-2">{{ subject.name }}</div>
              <div class="facts t-body-m">
                <span v-if="subject.rating.score" class="score"><Icon name="star" :size="16" />{{ subject.rating.score.toFixed(1) }}</span>
                <span v-if="subject.rating.rank" class="muted">排名 {{ subject.rating.rank }}</span>
                <span v-if="subject.date" class="muted">{{ subject.date }}</span>
                <span v-if="subject.eps" class="muted">{{ subject.eps }} 话</span>
              </div>
              <div v-if="airLine" class="air t-body-m">
                <Icon name="schedule" :size="18" />
                <span>{{ airLine }}<span v-if="airCn" class="muted"> · {{ airCn }}</span></span>
              </div>
              <div class="actions">
                <FollowButton :subject="subject" small />
              </div>
            </div>
          </div>
        </div>

        <div v-if="!subject" class="body pad">
          <div class="skeleton sect-sk" />
        </div>
        <div v-else class="body pad">
          <div v-if="subject.tags.length" v-reveal="0" class="tags">
            <Chip v-for="t in subject.tags" :key="t.name">{{ t.name }}</Chip>
          </div>

          <section v-if="subject.summary" v-reveal="1" class="fc-card sect">
            <h3 class="t-title-m">简介</h3>
            <p ref="summaryEl" class="summary t-body-m" :class="{ folded: overflowing && !expanded }">{{ subject.summary }}</p>
            <button v-if="overflowing" v-ripple class="more t-label-l" type="button" @click="toggleSummary">
              {{ expanded ? '收起' : '展开' }}
              <Icon name="expandMore" :size="20" class="chev" :class="{ up: expanded }" />
            </button>
          </section>

          <section v-if="info.length" v-reveal="2" class="fc-card sect">
            <h3 class="t-title-m">制作信息</h3>
            <dl class="info t-body-m">
              <template v-for="i in info" :key="i.key">
                <dt class="muted">{{ i.key }}</dt>
                <dd>{{ i.value }}</dd>
              </template>
            </dl>
          </section>
        </div>
      </template>
    </div>

    <router-link v-if="subject" v-ripple class="fab t-label-l" :to="`/play/${id}`">
      <Icon name="play" />
      开始观看
    </router-link>
  </div>
</template>

<style scoped>
.detail { position: absolute; inset: 0; overflow: hidden; isolation: isolate; background: var(--md-sys-color-surface); }
.detail-page { z-index: 1; background: transparent; }
.pad { padding-left: 16px; padding-right: 16px; max-width: 900px; margin: 0 auto; }
.state { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 64px 0; }
.hero { position: relative; overflow: hidden; }
.backdrop { position: absolute; inset: 0; pointer-events: none; }
.backdrop img {
  position: absolute;
  inset: -64px;
  width: calc(100% + 128px);
  height: calc(100% + 128px);
  max-width: none;
  object-fit: cover;
  object-position: center;
  filter: blur(32px) saturate(0.9);
  opacity: 0;
  transition: opacity var(--md-dur-medium) linear;
}
.backdrop img.show { opacity: 0.3; }
.backdrop::after {
  content: "";
  position: absolute;
  inset: 0;
  background: linear-gradient(
    180deg,
    color-mix(in srgb, var(--md-sys-color-surface) 36%, transparent),
    color-mix(in srgb, var(--md-sys-color-surface) 16%, transparent) 45%,
    color-mix(in srgb, var(--md-sys-color-surface) 44%, transparent)
  );
}
.detail-page :deep(.detail-appbar) { background: transparent; }
.detail-page :deep(.detail-appbar::before) {
  content: "";
  position: absolute;
  inset: 0 0 -48px;
  z-index: -1;
  pointer-events: none;
  background: linear-gradient(
    180deg,
    color-mix(in srgb, var(--md-sys-color-surface) 90%, transparent),
    color-mix(in srgb, var(--md-sys-color-surface) 80%, transparent) 60%,
    transparent
  );
  opacity: 0;
  transition: opacity var(--md-dur-medium) linear;
}
.detail-page :deep(.detail-appbar.on::before) { opacity: 1; }
.top { position: relative; display: flex; gap: 16px; padding-top: 8px; padding-bottom: 20px; }
.poster, .poster-sk { position: relative; flex: none; width: 120px; aspect-ratio: 2 / 3; border-radius: 14px; overflow: hidden; box-shadow: var(--fc-float); background: var(--md-sys-color-surface-container-high); }
.fill { position: absolute; inset: 0; border-radius: 0; }
.poster img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; opacity: 0; transition: opacity var(--md-dur-medium) linear; }
.poster img.show { opacity: 1; }
.head { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 6px; justify-content: flex-end; }
.head h2 { margin: 0; }
.facts { display: flex; flex-wrap: wrap; gap: 4px 12px; align-items: center; margin-top: 4px; }
.air { display: flex; align-items: flex-start; gap: 6px; color: var(--md-sys-color-primary); font-weight: 600; }
.air > .m3-icon { flex: none; margin-top: 2px; }
.air .muted { font-weight: 400; }
.actions { display: flex; gap: 8px; margin-top: 6px; }
.score { display: inline-flex; align-items: center; gap: 2px; font-weight: 700; color: var(--md-sys-color-primary); }
.lines { flex: 1; display: flex; flex-direction: column; gap: 12px; justify-content: flex-end; }
.l1 { height: 28px; width: 80%; } .l2 { height: 18px; width: 50%; }
.body { padding-bottom: 112px; display: flex; flex-direction: column; gap: 14px; }
.sect { padding: 16px; }
.tags { display: flex; flex-wrap: wrap; gap: 8px; }
h3 { margin: 0 0 8px; }
.summary { margin: 0; white-space: pre-line; color: var(--md-sys-color-on-surface-variant); overflow: hidden; }
/* 收起时最后一行渐隐，代替省略号（高度变化由 useExpand 做） */
.summary.folded { mask-image: linear-gradient(#000 calc(100% - 1.6em), transparent); -webkit-mask-image: linear-gradient(#000 calc(100% - 1.6em), transparent); }
.chev { transition: transform 420ms var(--ease-spring); }
.chev.up { transform: rotate(180deg); }
.sect-sk { height: 160px; border-radius: var(--fc-radius); }
.l3 { height: 18px; width: 35%; }
.reveal-soft { animation: rise-in 520ms var(--ease-soft) both; --in-y: 10px; }
@media (prefers-reduced-motion: reduce) { .reveal-soft, .fab { animation: none; } }
.more { display: inline-flex; align-items: center; gap: 4px; margin-top: 4px; padding: 6px 12px 6px 8px; border-radius: var(--md-shape-md); color: var(--md-sys-color-primary); }
.info { display: grid; grid-template-columns: auto 1fr; gap: 8px 16px; margin: 0; }
.info dt, .info dd { margin: 0; }
/* 「开始观看」：资料到了才出来，从小弹到原样 */
.fab {
  animation: fab-in 600ms var(--ease-spring) 120ms both;
  position: absolute;
  right: 16px;
  bottom: calc(16px + var(--safe-bottom));
  z-index: 8;
  display: inline-flex;
  align-items: center;
  gap: 12px;
  height: 56px;
  padding: 0 20px 0 16px;
  border-radius: 18px;
  background: var(--md-sys-color-primary-container);
  color: var(--md-sys-color-on-primary-container);
  box-shadow: var(--md-elev-3);
}
@keyframes fab-in { from { opacity: 0; transform: translateY(20px) scale(0.6); } }
</style>
