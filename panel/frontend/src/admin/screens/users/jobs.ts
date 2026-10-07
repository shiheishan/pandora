import type { GenerationJob } from './opsSchemas'

// 批量生成任务（adminops.UserGenerationJob）的展示口径

export const JOB_STATUS_VIEW: Record<GenerationJob['status'], { label: string; tone: 'neutral' | 'info' | 'ok' | 'danger' }> = {
  queued: { label: '排队中', tone: 'neutral' },
  running: { label: '生成中', tone: 'info' },
  succeeded: { label: '已完成', tone: 'ok' },
  failed: { label: '已停止', tone: 'danger' },
}

/** 进度百分比（0–100，取整）：已生成的占总数 */
export function jobPercent(job: Pick<GenerationJob, 'total' | 'completed'>): number {
  if (job.total <= 0) return 0
  return Math.min(100, Math.floor((job.completed * 100) / job.total))
}

/** 任务是否已结束（不再轮询） */
export function jobFinished(job: Pick<GenerationJob, 'status'>): boolean {
  return job.status === 'succeeded' || job.status === 'failed'
}

/** 结果 CSV 的默认文件名（服务端的 Content-Disposition 缺席时用） */
export function jobFilename(job: Pick<GenerationJob, 'email_prefix' | 'created_at'>): string {
  return `users-${job.email_prefix}-${job.created_at.slice(0, 10)}.csv`
}
