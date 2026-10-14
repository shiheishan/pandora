/**
 * 门户读写压测：登录会话 + 恒定到达率的读场景与可选写场景（续费→支付回调或取消）。
 *
 * 登录限流：ratelimit.auth_per_min 缺省每 IP 每路由每分钟 10 次；nginx /v1/auth/ 约 12 次/分。
 * 压测前按手册放宽，或用 LOGIN_PACE_MS 放慢 setup 登录。access token 缺省约 15 分钟过期，遇 401 会重登一次。
 *
 * 环境变量：
 *   BASE_URL          门户源，必填
 *   USERS_CSV         users.csv，缺省 ./users.csv
 *   LOGIN_USERS       setup 登录人数，缺省 200，不超过 csv 行数
 *   WRITE_USERS       前 N 人专用于写场景，缺省 50
 *   LOGIN_PACE_MS     每次登录后 sleep 毫秒，缺省 0
 *   READ_RATE         读场景每秒到达数，缺省 20
 *   WRITE_RATE        写场景每秒到达数，缺省 1；0 则不建写场景
 *   DURATION          缺省 5m
 *   PRE_VUS / MAX_VUS 缺省 50 / 300
 *   EPAY_CODE         易支付渠道 code，与 EPAY_KEY_FILE 同时给才走回调
 *   EPAY_KEY_FILE     易支付密钥文件路径（init 阶段 readSecret）
 *
 * 阈值：http_req_failed、checks、portal_write_errors 失败率均为 0。
 *
 * 示例：
 * K6_INSECURE_SKIP_TLS_VERIFY=true BASE_URL=https://panel.example.com USERS_CSV=/root/perf/users.csv \
 *   LOGIN_USERS=200 WRITE_RATE=1 EPAY_CODE=epay-xxx EPAY_KEY_FILE=/root/perf/epay.key \
 *   k6 run portal.js
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Rate, Counter } from 'k6/metrics';
import {
  env,
  requireEnv,
  num,
  loadUsers,
  login,
  jsonHeaders,
  idemKey,
  parseQuery,
  epaySign,
  readSecret,
  TREND_STATS,
} from './lib.js';

const BASE_URL = requireEnv('BASE_URL').replace(/\/$/, '');
const USERS_CSV = env('USERS_CSV', './users.csv');
const LOGIN_USERS_CFG = num('LOGIN_USERS', 200);
const WRITE_USERS = num('WRITE_USERS', 50);
const LOGIN_PACE_MS = num('LOGIN_PACE_MS', 0);
const READ_RATE = num('READ_RATE', 20);
const WRITE_RATE = num('WRITE_RATE', 1);
const DURATION = env('DURATION', '5m');
const PRE_VUS = num('PRE_VUS', 50);
const MAX_VUS = num('MAX_VUS', 300);
const EPAY_CODE = env('EPAY_CODE', '');
const EPAY_KEY_FILE = env('EPAY_KEY_FILE', '');

const users = loadUsers(USERS_CSV);
const LOGIN_USERS = Math.min(LOGIN_USERS_CFG, users.length);
if (WRITE_USERS > LOGIN_USERS) {
  throw new Error('WRITE_USERS 不能大于 LOGIN_USERS');
}

const useEpay = EPAY_CODE !== '' && EPAY_KEY_FILE !== '';
const epayKey = useEpay ? readSecret(EPAY_KEY_FILE) : '';

const portalWriteErrors = new Rate('portal_write_errors');
const portalWriteSkipped = new Counter('portal_write_skipped');

const tokenOverrides = new Map();

function reqIdSnippet(res) {
  let rid = '';
  try {
    const j = res.json();
    if (j && j.req_id) {
      rid = String(j.req_id);
    }
  } catch (_) {
    /* ignore */
  }
  if (!rid) {
    const h = res.headers['X-Request-Id'] || res.headers['X-Request-ID'];
    if (h) {
      rid = String(h);
    }
  }
  const body = res.body ? String(res.body).slice(0, 200) : '';
  return (rid ? 'req_id=' + rid + ' ' : '') + body;
}

function sessionToken(sessionIdx, data) {
  const key = String(sessionIdx);
  if (tokenOverrides.has(key)) {
    return tokenOverrides.get(key);
  }
  return data.sessions[sessionIdx].token;
}

function authed(method, url, body, sessionIdx, tagName, extraHeaders, data) {
  const sess = data.sessions[sessionIdx];
  let token = sessionToken(sessionIdx, data);
  // 首次请求把 401 记为预期（随后重登重发），不计入 http_req_failed
  const opts = {
    headers: jsonHeaders(token, extraHeaders),
    tags: { name: tagName },
    responseCallback: http.expectedStatuses({ min: 200, max: 399 }, 401),
  };
  let res;
  if (method === 'GET') {
    res = http.get(url, opts);
  } else if (method === 'POST') {
    res = http.post(url, body, opts);
  } else {
    throw new Error('unsupported method ' + method);
  }
  if (res.status === 401) {
    const newTok = login(BASE_URL, sess.email, sess.password, 'POST /v1/auth/login');
    tokenOverrides.set(String(sessionIdx), newTok);
    opts.headers = jsonHeaders(newTok, extraHeaders);
    delete opts.responseCallback;
    if (method === 'GET') {
      res = http.get(url, opts);
    } else {
      res = http.post(url, body, opts);
    }
  }
  return res;
}

const scenarios = {
  reads: {
    executor: 'constant-arrival-rate',
    exec: 'reads',
    rate: READ_RATE,
    timeUnit: '1s',
    duration: DURATION,
    preAllocatedVUs: PRE_VUS,
    maxVUs: MAX_VUS,
  },
};
if (WRITE_RATE > 0) {
  scenarios.writes = {
    executor: 'constant-arrival-rate',
    exec: 'writes',
    rate: WRITE_RATE,
    timeUnit: '1s',
    duration: DURATION,
    preAllocatedVUs: Math.min(PRE_VUS, WRITE_USERS),
    maxVUs: MAX_VUS,
  };
}

export const options = {
  setupTimeout: '20m',
  summaryTrendStats: TREND_STATS,
  scenarios,
  thresholds: {
    http_req_failed: ['rate==0'],
    checks: ['rate==1'],
    portal_write_errors: ['rate==0'],
  },
};

export function setup() {
  const sessions = [];
  for (let i = 0; i < LOGIN_USERS; i++) {
    const u = users[i];
    const token = login(BASE_URL, u.email, u.password, 'POST /v1/auth/login');
    sessions.push({ email: u.email, password: u.password, token });
    if (LOGIN_PACE_MS > 0) {
      sleep(LOGIN_PACE_MS / 1000);
    }
  }
  return { sessions };
}

export function reads(data) {
  const readPool = LOGIN_USERS - WRITE_USERS;
  const sessionIdx =
    readPool > 0
      ? WRITE_USERS + (exec.scenario.iterationInTest % readPool)
      : 0;

  let res = authed(
    'GET',
    BASE_URL + '/v1/me/subscriptions',
    null,
    sessionIdx,
    'GET /v1/me/subscriptions',
    null,
    data,
  );
  check(res, { 'GET /v1/me/subscriptions 200': (r) => r.status === 200 });

  res = authed('GET', BASE_URL + '/v1/plans', null, sessionIdx, 'GET /v1/plans', null, data);
  check(res, { 'GET /v1/plans 200': (r) => r.status === 200 });

  res = authed(
    'GET',
    BASE_URL + '/v1/orders?limit=20',
    null,
    sessionIdx,
    'GET /v1/orders',
    null,
    data,
  );
  check(res, { 'GET /v1/orders 200': (r) => r.status === 200 });
}

export function writes(data) {
  const sessionIdx = exec.scenario.iterationInTest % WRITE_USERS;
  const fail = (res) => {
    portalWriteErrors.add(1);
    console.warn('portal write HTTP ' + res.status + ' ' + reqIdSnippet(res));
  };

  const listRes = authed(
    'GET',
    BASE_URL + '/v1/me/subscriptions',
    null,
    sessionIdx,
    'GET /v1/me/subscriptions',
    null,
    data,
  );
  if (listRes.status !== 200) {
    fail(listRes);
    return;
  }
  const subs = listRes.json('subscriptions') || [];
  let subId = null;
  for (const s of subs) {
    if (s && s.status === 'active') {
      subId = s.id;
      break;
    }
  }
  if (!subId) {
    portalWriteSkipped.add(1);
    return;
  }

  const renewBody = JSON.stringify({ price_id: '', use_balance: 0 });
  const renewRes = authed(
    'POST',
    BASE_URL + '/v1/me/subscriptions/' + subId + '/renew',
    renewBody,
    sessionIdx,
    'POST /v1/me/subscriptions/:id/renew',
    { 'Idempotency-Key': idemKey('perf-renew') },
    data,
  );
  if (renewRes.status !== 201) {
    fail(renewRes);
    return;
  }
  const orderId = renewRes.json('order_id');
  if (!orderId) {
    portalWriteErrors.add(1);
    console.warn('renew 201 无 order_id ' + reqIdSnippet(renewRes));
    return;
  }

  if (useEpay) {
    const payBody = JSON.stringify({ provider: EPAY_CODE, method: 'alipay' });
    const payRes = authed(
      'POST',
      BASE_URL + '/v1/orders/' + orderId + '/pay',
      payBody,
      sessionIdx,
      'POST /v1/orders/:id/pay',
      null,
      data,
    );
    if (payRes.status !== 201) {
      fail(payRes);
      return;
    }
    const redirectUrl = payRes.json('redirect_url');
    if (!redirectUrl) {
      portalWriteErrors.add(1);
      console.warn('pay 无 redirect_url ' + reqIdSnippet(payRes));
      return;
    }
    const q = parseQuery(redirectUrl);
    const pid = q.pid;
    const outTradeNo = q.out_trade_no;
    const money = q.money;
    if (!pid || !outTradeNo || !money) {
      portalWriteErrors.add(1);
      console.warn('redirect_url 缺 pid/out_trade_no/money');
      return;
    }
    const vu = exec.vu;
    const params = {
      pid,
      trade_no: 'PERF' + Date.now() + '-' + vu.idInTest + '-' + vu.iterationInScenario,
      out_trade_no: outTradeNo,
      type: 'alipay',
      money,
      trade_status: 'TRADE_SUCCESS',
    };
    const sign = epaySign(params, epayKey);
    const keys = Object.keys(params).sort();
    const parts = [];
    for (const k of keys) {
      parts.push(encodeURIComponent(k) + '=' + encodeURIComponent(params[k]));
    }
    parts.push('sign=' + encodeURIComponent(sign));
    parts.push('sign_type=MD5');
    const webhookUrl =
      BASE_URL + '/v1/webhooks/payments/' + encodeURIComponent(EPAY_CODE) + '?' + parts.join('&');
    const whRes = http.get(webhookUrl, { tags: { name: 'GET /v1/webhooks/payments/:code' } });
    const whOk = check(whRes, {
      'webhook success': (r) => r.body === 'success',
    });
    if (!whOk) {
      fail(whRes);
      return;
    }
    portalWriteErrors.add(0);
  } else {
    const cancelRes = authed(
      'POST',
      BASE_URL + '/v1/orders/' + orderId + '/cancel',
      '{}',
      sessionIdx,
      'POST /v1/orders/:id/cancel',
      null,
      data,
    );
    if (cancelRes.status !== 200) {
      fail(cancelRes);
      return;
    }
    portalWriteErrors.add(0);
  }
}
