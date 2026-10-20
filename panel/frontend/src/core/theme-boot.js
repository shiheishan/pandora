// 为什么不写进入口模块：module 脚本是延迟执行的，浏览器可能先按浅色画一帧，
// 深色用户每次打开都会闪白。CSP 没有 unsafe-inline，不能用内联脚本，
// 所以它是一个独立的外链经典脚本，阻塞解析、只做首帧前必须做的事。
(function () {
  var theme = 'light'
  var storage = null
  try {
    storage = window.localStorage
    if (storage.getItem('pandora-theme') === 'dark') theme = 'dark'
  } catch {
    // 隐私模式等禁用存储的环境：按默认浅色
  }
  var root = document.documentElement
  root.setAttribute('data-theme', theme)

  // 门户首页片段（服务端插在 #root 之前，见 portal/home.ts）：已登录（有门户令牌）、地址带 hash 页面或邀请码时，
  // 首帧前先藏起来（<html data-home="off">，样式在 portal/home.css），免得已登录用户和直接进登录页的访客闪一下首页；
  // 真值由 App.tsx 挂载后按登录状态与地址再定。只在门户入口；后台与门户同源，不碰后台。
  // 地址与令牌分两步读：存储读不了（隐私模式）时地址照样算数
  if (root.getAttribute('data-app') === 'portal') {
    var homeOff = false
    try {
      var hash = window.location.hash || ''
      homeOff = (hash !== '' && hash !== '#' && hash !== '#/') || /[?&]invite=/.test(window.location.search || '')
    } catch {
      // 读不到地址：按没有页面
    }
    try {
      if (!homeOff && storage && storage.getItem('pandora-portal-token')) homeOff = true
    } catch {
      // 读不到存储：按未登录，App 挂载后再定
    }
    if (homeOff) root.setAttribute('data-home', 'off')
  }

  // 门户的生效主题（后台「外观」里切的那套颜色）：上次从 v1/appearance 拿到的值由
  // portal/appearance.ts 缓存在 pandora-portal-appearance，这里在首帧前写到 <html> 上，
  // 自定义主题的站点不再先画默认色、等接口回来再换色。只在门户入口（<html data-app="portal">）；
  // 后台与门户同源、共用 localStorage，不能把门户的颜色套到后台上。
  // 走 CSSOM（style.setProperty），不受 style-src 约束；只认 -- 开头的属性名。
  // 真值仍以接口为准：appearance.ts 拿到后覆盖页面上的值并刷新缓存
  try {
    if (!storage || root.getAttribute('data-app') !== 'portal') return
    var cached = JSON.parse(storage.getItem('pandora-portal-appearance') || 'null')
    var tokens = cached && cached.v === 1 ? cached[theme] : null
    if (!tokens || typeof tokens !== 'object') return
    var names = Object.keys(tokens)
    for (var i = 0; i < names.length; i++) {
      var value = tokens[names[i]]
      if (/^--[a-z0-9-]+$/.test(names[i]) && typeof value === 'string' && value.length <= 200) {
        root.style.setProperty(names[i], value)
      }
    }
  } catch {
    // 缓存坏了：按默认主题画，接口回来后照常套上
  }
})()
