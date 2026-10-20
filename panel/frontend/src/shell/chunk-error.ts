// ---------------------------------------------------------------------------
// 懒加载的页面块在发版后可能已被新哈希替换（旧标签页里点进一个没加载过的模块），
// React.lazy 会缓存这次失败，重置边界无济于事，只能整页刷新拿新入口。
// ---------------------------------------------------------------------------
export function isChunkLoadError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return /dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(error.message)
}
