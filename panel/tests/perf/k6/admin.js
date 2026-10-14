/**
 * 后台列表与看板读压测：恒定到达率轮流打 7 类 GET。
 *
 * 环境变量：
 *   ADMIN_BASE_URL       后台源+路径前缀，必填，如 https://panel.example.com/admin-xxxx
 *   ADMIN_PASSWORD_FILE  管理员口令文件，必填（init readSecret）
 *   ADMIN_EMAIL          或 ADMIN_ENV_FILE 提供邮箱
 *   RATE                 每秒到达数，缺省 5
 *   DURATION             缺省 5m
 *   PRE_VUS / MAX_VUS    缺省 20 / 100
 *   USER_PAGES           用户列表随机翻页上限（每页 25），缺省 200
 *
 * 查询参数（与 report/handler/dto.go 一致）：
 *   traffic nodes/users 缺省 range=7d、limit=10（允许 24h|7d|30d，limit 5|10|20）
 *   stats/timeseries 缺省 days=14（0 或未传时 service 也用 14，1–90）
 *
 * 阈值：http_req_failed、checks 失败率均为 0。
 *
 * 示例：
 * K6_INSECURE_SKIP_TLS_VERIFY=true ADMIN_BASE_URL=https://panel.example.com/admin-xxxx \
 *   ADMIN_PASSWORD_FILE=/root/perf/admin.pass ADMIN_ENV_FILE=/root/perf/admin.env \
 *   k6 run admin.js
 */
import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';
import {
  env,
  requireEnv,
  num,
  adminEmail,
  readSecret,
  login,
  jsonHeaders,
  TREND_STATS,
} from './lib.js';

const ADMIN_BASE = requireEnv('ADMIN_BASE_URL').replace(/\/$/, '');
const ADMIN_PASS = readSecret(requireEnv('ADMIN_PASSWORD_FILE'));
const ADMIN_MAIL = adminEmail();
const RATE = num('RATE', 5);
const DURATION = env('DURATION', '5m');
const PRE_VUS = num('PRE_VUS', 20);
const MAX_VUS = num('MAX_VUS', 100);
const USER_PAGES = num('USER_PAGES', 200);

let setupToken = '';
const vuTokens = new Map();

function adminToken() {
  const id = exec.vu.idInTest;
  if (vuTokens.has(id)) {
    return vuTokens.get(id);
  }
  return setupToken;
}

function adminGet(path, tagName) {
  const url = ADMIN_BASE + path;
  // 首次请求把 401 记为预期（随后重登重发），不计入 http_req_failed
  const opts = {
    headers: jsonHeaders(adminToken()),
    tags: { name: tagName },
    responseCallback: http.expectedStatuses({ min: 200, max: 399 }, 401),
  };
  let res = http.get(url, opts);
  if (res.status === 401) {
    const tok = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
    vuTokens.set(exec.vu.idInTest, tok);
    opts.headers = jsonHeaders(tok);
    delete opts.responseCallback;
    res = http.get(url, opts);
  }
  check(res, { [tagName + ' 200']: (r) => r.status === 200 });
  return res;
}

export const options = {
  summaryTrendStats: TREND_STATS,
  scenarios: {
    lists: {
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
  },
};

export function setup() {
  setupToken = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
  return { token: setupToken };
}

export default function (data) {
  if (data && data.token) {
    setupToken = data.token;
  }
  const n = exec.scenario.iterationInTest % 7;
  const page = Math.floor(Math.random() * USER_PAGES);
  const offset = page * 25;

  switch (n) {
    case 0:
      adminGet('/v1/users?limit=25&offset=' + offset, 'GET /admin/v1/users');
      break;
    case 1:
      adminGet('/v1/orders?limit=25', 'GET /admin/v1/orders');
      break;
    case 2:
      adminGet('/v1/overview', 'GET /admin/v1/overview');
      break;
    case 3:
      adminGet('/v1/dashboard/traffic/nodes?range=7d&limit=10', 'GET /admin/v1/dashboard/traffic/nodes');
      break;
    case 4:
      adminGet('/v1/dashboard/traffic/users?range=7d&limit=10', 'GET /admin/v1/dashboard/traffic/users');
      break;
    case 5:
      adminGet('/v1/dashboard/tasks', 'GET /admin/v1/dashboard/tasks');
      break;
    case 6:
      adminGet('/v1/stats/timeseries?days=14', 'GET /admin/v1/stats/timeseries');
      break;
    default:
      break;
  }
}
