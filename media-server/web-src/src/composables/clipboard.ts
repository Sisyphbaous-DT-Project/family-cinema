// 复制到剪贴板。盒子是内网 http 地址，不算"安全环境"，浏览器和 App 的 WebView 里都没有
// navigator.clipboard，只能用老办法（隐藏文本框 + execCommand）。老办法必须在点击事件里同步执行，
// 所以这里在任何 await 之前就先走老办法。
export function copyText(text: string): boolean {
  if (window.isSecureContext && navigator.clipboard) {
    navigator.clipboard.writeText(text).catch(() => legacyCopy(text))
    return true
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // 放在屏幕内但看不见：放到屏幕外有些 WebView 会拒绝选中
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none;'
  document.body.appendChild(ta)
  ta.select()
  ta.setSelectionRange(0, text.length)
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  return ok
}
