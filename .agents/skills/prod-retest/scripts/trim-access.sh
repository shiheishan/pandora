#!/usr/bin/env bash
# 把场景目录里整份拷来的 nginx access log 裁到 [T−6m, T+W+10m]：trim-access.sh <场景目录> [稳态分钟=30]
# T 取自 timeline.txt 第一行的「T=...」。需要 awk 的 mktime（Debian 的 mawk 1.3.4 与 gawk 都有）。
P=${1:?场景目录}; W=${2:-30}
T=$(sed -n "s/.*T=\([0-9T:-]*Z\).*/\1/p" "$P/timeline.txt" | head -1)
from=$(date -u -d "$T - 6 min" +%s); to=$(date -u -d "$T + $((W + 10)) min" +%s)
for f in "$P"/*access*.log; do
  [ -f "$f" ] || continue
  awk -v a="$from" -v b="$to" '{
    if (match($0, /\[[0-9]+\/[A-Za-z]+\/[0-9]+:[0-9:]+/)) {
      s = substr($0, RSTART + 1, RLENGTH - 1); split(s, p, /[\/:]/)
      m = (index("JanFebMarAprMayJunJulAugSepOctNovDec", p[2]) + 2) / 3
      t = mktime(p[3] " " m " " p[1] " " p[4] " " p[5] " " p[6])
      if (t >= a && t <= b) print
    }
  }' "$f" > "$f.win" && mv "$f.win" "$f"
done
du -sh "$P"/*access*.log
