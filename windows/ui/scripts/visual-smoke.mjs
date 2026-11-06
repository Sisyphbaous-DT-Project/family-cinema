import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const output = process.env.UI_SCREENSHOTS || path.join(os.tmpdir(), 'family-cinema-ui-shots')
fs.mkdirSync(output, { recursive: true })
const modulePath = process.env.PLAYWRIGHT_MODULE
const { chromium } = await import(modulePath ? pathToFileURL(modulePath).href : 'playwright')
const html = fs.readFileSync(path.join(root, 'dist/index.html'))
const server = http.createServer((_request, response) => {
  response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
  response.end(html)
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = `http://127.0.0.1:${server.address().port}`
const browser = await chromium.launch({ headless: true, channel: 'chromium' })
const errors = []

async function createPage({ state = 'fresh', width = 900, height = 760, dark = false, host = true } = {}) {
  const context = await browser.newContext({ viewport: { width, height }, colorScheme: dark ? 'dark' : 'light' })
  const page = await context.newPage()
  page.on('pageerror', error => errors.push(error.message))
  const requests = []
  page.on('request', request => requests.push(request.url()))
  if (host) await page.addInitScript(({ scenario }) => {
    const listeners = []
    const requests = []
    window.__hostRequests = requests
    let cancelled = false
    let options = { Root: 'C:\\Users\\User\\AppData\\Local\\FamilyCinema', Media: 'D:\\FamilyCinema',
      Pansou: true, FFmpeg: false, Lan: true, AutoStart: false, Port: 8088, PansouPort: 18888 }
    let installed = ['running', 'warning'].includes(scenario)
    let running = installed
    const send = data => listeners.forEach(callback => callback({ data }))
    const snapshot = () => ({ version: '1.11.0', options: { ...options }, installed, running,
      ffmpegAvailable: true, warnings: scenario === 'warning' ? ['盘搜下载未完成，主程序仍可使用。请重新安装以修复。'] : [],
      address: running ? 'http://127.0.0.1:8088' : '' })
    window.chrome = window.chrome || {}
    window.chrome.webview = {
      addEventListener: (_event, callback) => listeners.push(callback),
      postMessage: async request => {
        requests.push(request)
        const { id, method, args } = request
        if (method === 'init') {
          if (args.root) options.Root = args.root
          send({ id, result: snapshot() })
        } else if (method === 'browse') {
          send({ id, result: args.kind === 'root' ? 'C:\\家庭影院 程序' : 'D:\\家庭影院 媒体' })
        } else if (method === 'install') {
          cancelled = false
          send({ type: 'progress', message: '镜像测速：正在选择可用下载源' })
          await new Promise(resolve => setTimeout(resolve, 300))
          if (cancelled) send({ id, error: '操作已取消，本次临时文件已清理。' })
          else if (scenario === 'error') send({ id, error: '源码下载未完成，原有程序未被替换。请稍后重试。' })
          else {
            options = args
            installed = running = true
            send({ id, result: snapshot() })
          }
        } else if (method === 'cancel') {
          cancelled = true
          send({ id, result: null })
        } else if (method === 'start' || method === 'stop') {
          running = method === 'start'
          send({ id, result: snapshot() })
        } else send({ id, result: null })
      },
    }
  }, { scenario: state })
  await page.goto(origin)
  await page.getByRole('heading', { name: '家庭影院', exact: true }).waitFor()
  if (host) await page.getByRole('textbox', { name: '程序文件夹' }).waitFor()
  await page.waitForTimeout(100)
  return { page, context, requests }
}

async function checkFrame(page) {
  const metrics = await page.evaluate(() => {
    const footer = document.querySelector('.action-bar').getBoundingClientRect()
    const main = document.querySelector('.main-scroll').getBoundingClientRect()
    const overflowing = [...document.querySelectorAll('button, label, h1, h2, h3')].filter(element => {
      const bounds = element.getBoundingClientRect()
      return bounds.width > 0 && (bounds.left < -1 || bounds.right > innerWidth + 1)
    }).map(element => element.textContent.trim())
    return { width: innerWidth, scroll: document.documentElement.scrollWidth,
      footerTop: footer.top, mainBottom: main.bottom, footerBottom: footer.bottom, height: innerHeight, overflowing }
  })
  assert.equal(metrics.scroll, metrics.width, 'horizontal page overflow')
  assert.ok(metrics.mainBottom <= metrics.footerTop + 1, 'main content overlaps the fixed action bar')
  assert.ok(metrics.footerBottom <= metrics.height + 1, 'primary action is outside the window')
  assert.deepEqual(metrics.overflowing, [], 'controls leave the window horizontally')
}

try {
  {
    const { page, context, requests } = await createPage()
    assert.equal(await page.getByRole('checkbox', { name: '网盘搜索' }).isChecked(), true)
    assert.equal(await page.getByRole('checkbox', { name: '声音与字幕处理' }).isChecked(), false)
    assert.equal(await page.getByRole('button', { name: '开始安装' }).isEnabled(), true)
    await checkFrame(page)
    await page.screenshot({ path: path.join(output, 'fresh-900x760.png'), fullPage: true })
    await page.getByRole('checkbox', { name: '声音与字幕处理' }).click()
    await page.getByRole('heading', { name: '启用声音与字幕处理？' }).waitFor()
    await page.getByRole('button', { name: '启用处理', exact: true }).click()
    assert.equal(await page.getByRole('checkbox', { name: '声音与字幕处理' }).isChecked(), true)
    await page.getByRole('button', { name: '选择媒体文件夹' }).click()
    assert.equal(await page.getByRole('textbox', { name: '媒体文件夹' }).inputValue(), 'D:\\家庭影院 媒体')
    await page.getByRole('button', { name: '开始安装' }).click()
    await page.getByRole('button', { name: '取消操作' }).waitFor()
    assert.equal(await page.getByRole('textbox', { name: '程序文件夹' }).isDisabled(), true)
    assert.equal(await page.getByRole('checkbox', { name: '网盘搜索' }).isDisabled(), true)
    await page.screenshot({ path: path.join(output, 'busy-900x760.png'), fullPage: true })
    await page.getByRole('button', { name: '取消操作' }).click()
    await page.getByText('操作已取消', { exact: true }).waitFor()
    await page.getByRole('button', { name: '开始安装' }).click()
    await page.getByRole('button', { name: '安装 / 更新' }).waitFor()
    await page.getByRole('button', { name: '下载手机 App' }).click()
    await page.getByRole('button', { name: '允许手机访问', exact: true }).click()
    await page.getByRole('button', { name: '继续', exact: true }).click()
    await page.getByRole('button', { name: '停止', exact: true }).click()
    await page.getByRole('button', { name: '启动', exact: true }).click()
    const methods = await page.evaluate(() => window.__hostRequests.map(request => request.method))
    for (const method of ['init', 'browse', 'install', 'cancel', 'openWeb', 'firewall', 'stop', 'start']) assert.ok(methods.includes(method), `${method} did not reach the host bridge`)
    assert.equal(requests.length, 1, 'the production HTML requested an external asset')
    await context.close()
    console.log('PASS 900x760 first install, default PanSou, FFmpeg confirmation, browse, cancel, success, bridge actions, no external assets')
  }
  {
    const { page, context } = await createPage({ state: 'warning', width: 680, height: 600, dark: true })
    await page.getByText('部分组件需要重试', { exact: true }).waitFor()
    await checkFrame(page)
    assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), 'dark')
    await page.screenshot({ path: path.join(output, 'warning-dark-680x600.png'), fullPage: true })
    await page.getByRole('button', { name: '外观', exact: true }).click()
    await page.getByRole('menuitemradio', { name: '浅色' }).click()
    assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), 'light')
    await page.waitForTimeout(200)
    await page.locator('.main-scroll').evaluate(element => { element.scrollTop = element.scrollHeight })
    await page.screenshot({ path: path.join(output, 'scrolled-light-680x600.png'), fullPage: true })
    await checkFrame(page)
    await context.close()
    console.log('PASS 680x600 dark/light, running and partial-component warning, scrolling without footer overlap')
  }
  {
    const { page, context } = await createPage({ state: 'error' })
    await page.getByRole('button', { name: '开始安装' }).click()
    await page.getByText('操作未完成', { exact: true }).waitFor()
    assert.equal(await page.getByRole('button', { name: '开始安装' }).isEnabled(), true)
    assert.equal(await page.locator('.log-body').isVisible(), true)
    await page.waitForTimeout(250)
    await page.screenshot({ path: path.join(output, 'failure-900x760.png'), fullPage: true })
    await context.close()
    console.log('PASS install failure reports error, expands log and permits retry')
  }
  {
    const { page, context } = await createPage({ host: false })
    await page.getByRole('heading', { name: '请在一键程序中打开' }).waitFor()
    assert.equal(await page.getByRole('button', { name: '开始安装' }).isDisabled(), true)
    await context.close()
    console.log('PASS production page without host shows connection failure instead of simulated success')
  }
  assert.deepEqual(errors, [], 'browser runtime errors')
  console.log(`Screenshots: ${output}`)
} finally {
  await browser.close()
  await new Promise(resolve => server.close(resolve))
}
