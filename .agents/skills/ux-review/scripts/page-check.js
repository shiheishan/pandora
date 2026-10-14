// 体验审查的逐页检查：把整个文件作为 javascript_tool 的 text 执行，返回 JSON。
// 只读，不点任何东西。查三件事：
//   overflow   页面是否横向滚动，以及右边超出视口的元素（最多 10 个）
//   words      禁用词命中：可见文字、打开着的 <dialog>、aria-label / title / placeholder / alt
//   unnamed    没有可读名字的按钮和链接（键盘与读屏用户看不出它是干什么的）
// 禁用词以原型 flow.md 第 7 节与 .claude/rules/screens-portal.md 的叫法一条为准，改了那两处就改这里。
// 只用于门户；后台不扫禁用词（把 WORDS 置空）。
(() => {
  const WORDS = ['订阅', '导入', '抵扣', '折算', '剩余价值', '降级', '客户端', '落点', '约付', '周期', '重置订阅', '重置链接']
  // 「升级」只许出现在兑换不同款套餐卡的选项里：命中时列出来人工核
  const REVIEW = ['升级']

  const vw = document.documentElement.clientWidth
  const overflow = {
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: vw,
    horizontal: document.documentElement.scrollWidth > vw + 1,
    offenders: [],
  }
  for (const el of document.body.querySelectorAll('*')) {
    const r = el.getBoundingClientRect()
    if (r.width > 0 && r.right > vw + 1) {
      const cs = getComputedStyle(el)
      if (cs.position === 'fixed' && cs.visibility === 'hidden') continue
      overflow.offenders.push({
        tag: el.tagName.toLowerCase(),
        cls: String(el.className).slice(0, 60),
        right: Math.round(r.right),
        text: (el.textContent || '').trim().slice(0, 40),
      })
      if (overflow.offenders.length >= 10) break
    }
  }

  const sources = []
  sources.push({ where: 'body', text: document.body.innerText || '' })
  for (const d of document.querySelectorAll('dialog[open]')) sources.push({ where: 'dialog', text: d.innerText || '' })
  for (const el of document.querySelectorAll('[aria-label],[title],[placeholder],img[alt]')) {
    for (const a of ['aria-label', 'title', 'placeholder', 'alt']) {
      const v = el.getAttribute(a)
      if (v) sources.push({ where: `@${a}`, text: v })
    }
  }
  sources.push({ where: 'document.title', text: document.title })

  const hits = (list) => {
    const out = []
    for (const w of list) {
      for (const s of sources) {
        let i = s.text.indexOf(w)
        while (i !== -1) {
          out.push({ word: w, where: s.where, context: s.text.slice(Math.max(0, i - 15), i + w.length + 15).replace(/\s+/g, ' ') })
          i = s.text.indexOf(w, i + w.length)
        }
      }
    }
    // body 的 innerText 已包含打开的 dialog，去重
    const seen = new Set()
    return out.filter((h) => {
      const k = `${h.word}|${h.context}`
      if (seen.has(k)) return false
      seen.add(k)
      return true
    })
  }

  const unnamed = []
  for (const el of document.querySelectorAll('button, a[href], [role=button]')) {
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    const name = (el.getAttribute('aria-label') || el.getAttribute('title') || el.innerText || '').trim()
    if (!name) unnamed.push({ tag: el.tagName.toLowerCase(), cls: String(el.className).slice(0, 60), html: el.outerHTML.slice(0, 100) })
  }

  return {
    url: location.href,
    viewport: `${vw}x${document.documentElement.clientHeight}`,
    theme: document.documentElement.getAttribute('data-theme') || 'light',
    overflow,
    words: hits(WORDS),
    review: hits(REVIEW),
    unnamed: unnamed.slice(0, 10),
    unnamedCount: unnamed.length,
  }
})()
