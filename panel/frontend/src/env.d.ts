/**
 * [INPUT]: 无
 * [OUTPUT]: 对外声明构建期常量 __APP_RELEASE__
 * [POS]: src 的全局类型补充，值由 vite.config.ts 的 define 注入（PANDORA_RELEASE，本地为 dev）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
declare const __APP_RELEASE__: string
