// [INPUT]: 依赖浏览器 localStorage 的 pandora-theme 键（与 ./theme.ts 的 THEME_STORAGE_KEY 同值，tests/theme-boot.test.ts 守住）
// [OUTPUT]: 无导出；在首帧之前给 <html> 写上 data-theme="light|dark"
// [POS]: core 的主题引导脚本，由 vite.config.ts 的 themeBoot 插件按内容哈希输出到 assets/ 并以经典 <script src> 注入 <head>；此后由 theme.ts 接管
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
//
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
