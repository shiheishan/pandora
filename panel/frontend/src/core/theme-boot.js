// 为什么不写进入口模块：module 脚本是延迟执行的，浏览器可能先按浅色画一帧，
// 深色用户每次打开都会闪白。CSP 没有 unsafe-inline，不能用内联脚本，
// 所以它是一个独立的外链经典脚本，阻塞解析、只做这一件事。
(function () {
  var theme = 'light'
  try {
    if (window.localStorage.getItem('pandora-theme') === 'dark') theme = 'dark'
  } catch {
    // 隐私模式等禁用存储的环境：按默认浅色
  }
  document.documentElement.setAttribute('data-theme', theme)
})()
