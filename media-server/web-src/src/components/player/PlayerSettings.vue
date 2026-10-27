<script setup lang="ts">
// 播放器设置面板：倍速/音量/画面/滤镜/步长/行为开关。两种摆法（由 VideoPlayer 用 Teleport 切换）：
//  - floating（不全屏）：搬到 body，悬浮在屏幕正中间，和 App 其它卡片一个颜色（播放器框太小，关在里面会被切掉）
//  - 全屏：必须留在播放器 .stage 里面（全屏时只显示 .stage 里的东西），深色半透明，悬浮在画面右边
import { playerPrefs, resetPlayerPrefs, type PlayerPrefs } from '../../composables/playerPrefs'
import { toast } from '../../composables/snackbar'
import Switch from '../m3/Switch.vue'
import IconButton from '../m3/IconButton.vue'

defineProps<{ floating?: boolean }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'pip'): void }>()

const SPEEDS = [0.5, 0.75, 1, 1.25, 1.5, 2, 3]
const FITS: { value: PlayerPrefs['fit']; label: string }[] = [
  { value: 'contain', label: '原始比例' },
  { value: 'cover', label: '填充裁切' },
  { value: 'fill', label: '拉伸铺满' },
]
const canPip = typeof document !== 'undefined' && 'pictureInPictureEnabled' in document && document.pictureInPictureEnabled

function reset() {
  resetPlayerPrefs()
  toast('已恢复默认设置')
}
</script>

<template>
  <div class="pscrim" :class="{ floating }" @pointerdown.stop @pointerup.stop @click="emit('close')">
    <section class="panel" @click.stop>
      <div class="phead">
        <span class="t-title-m">播放设置</span>
        <IconButton icon="close" label="关闭" @click="emit('close')" />
      </div>
      <div class="pbody">
        <div class="group">
          <div class="glabel t-label-l">倍速</div>
          <div class="chips">
            <button
              v-for="s in SPEEDS"
              :key="s"
              v-ripple
              class="chip t-label-l"
              :class="{ on: playerPrefs.rate === s }"
              type="button"
              @click="playerPrefs.rate = s"
            >{{ s }}x</button>
          </div>
        </div>

        <div class="group">
          <div class="glabel t-label-l">画面适应</div>
          <div class="chips">
            <button
              v-for="f in FITS"
              :key="f.value"
              v-ripple
              class="chip t-label-l"
              :class="{ on: playerPrefs.fit === f.value }"
              type="button"
              @click="playerPrefs.fit = f.value"
            >{{ f.label }}</button>
          </div>
        </div>

        <div class="group">
          <div class="glabel t-label-l">音量 <span class="val">{{ Math.round(playerPrefs.vol * 100) }}%</span></div>
          <input v-model.number="playerPrefs.vol" type="range" min="0" max="1" step="0.05" />
        </div>
        <div class="group">
          <div class="glabel t-label-l">亮度 <span class="val">{{ playerPrefs.bri }}%</span></div>
          <input v-model.number="playerPrefs.bri" type="range" min="40" max="180" step="5" />
        </div>
        <div class="group">
          <div class="glabel t-label-l">对比度 <span class="val">{{ playerPrefs.con }}%</span></div>
          <input v-model.number="playerPrefs.con" type="range" min="50" max="180" step="5" />
        </div>
        <div class="group">
          <div class="glabel t-label-l">饱和度 <span class="val">{{ playerPrefs.sat }}%</span></div>
          <input v-model.number="playerPrefs.sat" type="range" min="0" max="200" step="5" />
        </div>
        <div class="group">
          <div class="glabel t-label-l">锐度 <span class="val">{{ playerPrefs.sharp }}</span></div>
          <input v-model.number="playerPrefs.sharp" type="range" min="0" max="100" step="5" />
        </div>
        <div class="group">
          <div class="glabel t-label-l">快进步长 <span class="val">{{ playerPrefs.seek }} 秒</span></div>
          <input v-model.number="playerPrefs.seek" type="range" min="5" max="60" step="5" />
        </div>

        <div class="srow">
          <span class="t-body-m">单集循环</span>
          <Switch v-model="playerPrefs.loop" />
        </div>
        <div class="srow">
          <span class="t-body-m">自动连播下一集</span>
          <Switch v-model="playerPrefs.auto" />
        </div>
        <div class="srow">
          <span class="t-body-m">记忆播放进度</span>
          <Switch v-model="playerPrefs.resume" />
        </div>
        <div class="srow">
          <span class="t-body-m">水平镜像</span>
          <Switch v-model="playerPrefs.mirror" />
        </div>

        <div class="foot">
          <button v-if="canPip" v-ripple class="fbtn t-label-l" type="button" @click="emit('pip')">画中画</button>
          <button v-ripple class="fbtn t-label-l" type="button" @click="reset">恢复默认</button>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* 颜色都走变量：全屏时盖在画面上用深色半透明；不全屏悬浮在页面中间时跟 App 的卡片同色（深浅色模式自动跟随） */
.pscrim {
  --ps-bg: rgb(24 24 24 / 0.94);
  --ps-fg: #fff;
  --ps-chip: rgb(255 255 255 / 0.12);
  --ps-accent: var(--md-sys-color-primary-container);
  position: absolute; inset: 0; z-index: 30;
  display: flex; justify-content: flex-end; align-items: stretch;
  padding: 12px;
  background: rgb(0 0 0 / 0.35);
}
.pscrim.floating {
  --ps-bg: var(--fc-card);
  --ps-fg: var(--md-sys-color-on-surface);
  --ps-chip: var(--md-sys-color-surface-container-high);
  --ps-accent: var(--md-sys-color-primary);
  position: fixed; z-index: 150;
  justify-content: center; align-items: center;
  padding: calc(24px + var(--safe-top)) 16px calc(24px + var(--safe-bottom));
  background: rgb(0 0 0 / 0.45);
  animation: ps-fade var(--md-dur-short) linear;
}
/* 面板本身：悬浮的圆角卡片 */
.panel {
  width: min(340px, 88%);
  display: flex; flex-direction: column;
  border-radius: var(--fc-radius);
  background: var(--ps-bg); color: var(--ps-fg);
  box-shadow: 0 8px 32px rgb(0 0 0 / 0.35);
  backdrop-filter: blur(8px);
  overflow: hidden;
}
.floating .panel { width: min(440px, 100%); max-height: min(100%, 620px); animation: ps-pop var(--md-dur-medium) var(--md-ease-emphasized-decel); }
.phead { display: flex; align-items: center; justify-content: space-between; padding: 10px 8px 4px 20px; }
.pbody { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 4px 20px 24px; display: flex; flex-direction: column; gap: 14px; }
.glabel { display: flex; justify-content: space-between; margin-bottom: 6px; opacity: 0.85; }
.val { opacity: 0.7; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; }
.chip { padding: 7px 14px; border-radius: var(--md-shape-md); background: var(--ps-chip); color: var(--ps-fg); }
.chip.on { background: var(--md-sys-color-primary-container); color: var(--md-sys-color-on-primary-container); }
input[type='range'] { width: 100%; accent-color: var(--ps-accent); height: 24px; }
.srow { display: flex; align-items: center; justify-content: space-between; min-height: 36px; }
.foot { display: flex; gap: 12px; justify-content: flex-end; margin-top: 4px; }
.fbtn { padding: 9px 18px; border-radius: var(--md-shape-md); background: var(--ps-chip); color: var(--ps-fg); }
@keyframes ps-fade { from { opacity: 0; } }
@keyframes ps-pop { from { opacity: 0; transform: scale(0.94); } }
</style>
