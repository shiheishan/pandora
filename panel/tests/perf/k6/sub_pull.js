/**
 * 订阅拉取压测：恒定到达率 GET /:prefix/:token（公开，不经 /v1）。
 *
 * 环境变量：
 *   BASE_URL      门户源，必填，无尾斜杠，如 https://panel.example.com
 *   SUB_PREFIX    12 位十六进制订阅路径前缀，必填
 *   USERS_CSV     perfseed 产出的 users.csv，缺省 ./users.csv
 *   RATE          每秒到达请求数，缺省 20
 *   DURATION      场景时长，缺省 5m
 *   PRE_VUS       预分配 VU，缺省 50
 *   MAX_VUS       最大 VU，缺省 500
 *   SUB_UA        User-Agent，缺省 mihomo/1.19.0
 *   SUB_UA_MIX    为 1 时按迭代轮换 mihomo / sing-box / v2rayN 三种 UA
 *   SUB_TARGET    非空时追加 ?target=…
 *
 * 阈值：http_req_failed、checks、sub_pull_errors 均为 0 失败率；
 *   http_req_duration{name:GET /:prefix/:token} 的 p(95)>=0 仅用于在汇总里单列该路由时序（不按令牌拆分 tags.name）。
 *
 * 示例：
 * K6_INSECURE_SKIP_TLS_VERIFY=true BASE_URL=https://panel.example.com SUB_PREFIX=xxxxxxxxxxxx \
 *   USERS_CSV=/root/perf/users.csv RATE=50 DURATION=5m \
 *   k6 run --summary-export=sub_pull.json sub_pull.js
 */
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import { Rate } from 'k6/metrics';
import {
  env,
  requireEnv,
  num,
  loadUsers,
  TREND_STATS,
} from './lib.js';

const BASE_URL = requireEnv('BASE_URL').replace(/\/$/, '');
const SUB_PREFIX = requireEnv('SUB_PREFIX');
const USERS_CSV = env('USERS_CSV', './users.csv');
const RATE = num('RATE', 20);
const DURATION = env('DURATION', '5m');
const PRE_VUS = num('PRE_VUS', 50);
const MAX_VUS = num('MAX_VUS', 500);
const SUB_UA = env('SUB_UA', 'mihomo/1.19.0');
const SUB_UA_MIX = env('SUB_UA_MIX', '') === '1';
const SUB_TARGET = env('SUB_TARGET', '');

const users = loadUsers(USERS_CSV);
const subPullErrors = new Rate('sub_pull_errors');

const UA_ROTATION = ['mihomo/1.19.0', 'sing-box 1.12.0', 'v2rayN/7.0'];

export const options = {
  summaryTrendStats: TREND_STATS,
  scenarios: {
    pull: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: PRE_VUS,
      maxVUs: MAX_VUS,
    },
  },
  thresholds: {
    http_req_failed: ['rate==0'],
    checks: ['rate==1'],
    sub_pull_errors: ['rate==0'],
    // 汇总里单独列出订阅拉取耗时，不设实际上限
    'http_req_duration{name:GET /:prefix/:token}': ['p(95)>=0'],
  },
};

function pickUa() {
  if (!SUB_UA_MIX) {
    return SUB_UA;
  }
  const i = exec.scenario.iterationInTest % UA_ROTATION.length;
  return UA_ROTATION[i];
}

export default function () {
  const idx = exec.scenario.iterationInTest % users.length;
  const u = users[idx];
  let url = BASE_URL + '/' + SUB_PREFIX + '/' + u.sub_token;
  if (SUB_TARGET) {
    url += '?target=' + encodeURIComponent(SUB_TARGET);
  }
  const res = http.get(url, {
    headers: { 'User-Agent': pickUa() },
    tags: { name: 'GET /:prefix/:token' },
  });
  const ok = check(res, {
    'status 200': (r) => r.status === 200,
    'body non-empty': (r) => r.body && r.body.length > 0,
  });
  subPullErrors.add(!ok);
}
