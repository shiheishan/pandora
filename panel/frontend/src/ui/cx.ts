/**
 * [INPUT]: 无依赖
 * [OUTPUT]: 对外提供 cx：拼接 className，跳过 false / null / undefined / 空串
 * [POS]: ui 的内部工具，各组件用它把 CSS Modules 的类名与调用方传入的 className 合并
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
export function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ')
}
