// Query cache lives only in memory and is partitioned by authenticated scope.
export function cachePolicy(path: string) {
  const route = (path.split("?")[0] || "").replace(/^\/+/, "");
  const base = { staleTime: 30_000, gcTime: 5 * 60_000,
    refetchOnWindowFocus: true, refetchOnReconnect: true,
    refetchInterval: false as number | false, refetchIntervalInBackground: false };
  if (/gift-cards\/codes|gift-cards\/usages|withdrawals|password|secret|token|bootstrap|settings\/mail|payment-providers|reauth/.test(route))
    return { ...base, staleTime: 0, gcTime: 0 };
  if (/orders|refund|balance|payment|commission|withdraw|subscriptions|overview|nodes|servers|metrics|usage|traffic/.test(route))
    return { ...base, staleTime: 0, gcTime: 60_000, refetchInterval: 30_000 };
  if (/^v1\/(?:me\/)?(?:content|help|announcements)(?:\/|$)/.test(route))
    return { ...base, staleTime: 10 * 60_000, gcTime: 30 * 60_000 };
  if (/^v1\/(?:me\/)?plans(?:\/|$)/.test(route))
    return { ...base, staleTime: 2 * 60_000, gcTime: 10 * 60_000 };
  return base;
}
