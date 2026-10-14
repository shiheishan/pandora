/**
 * 群发营销邮件 + 轮询通知积压直至清空或超时。
 * 建议在另一终端同时跑 sub_pull.js，观察在线订阅拉取延迟是否抬升。
 *
 * 环境变量：
 *   ADMIN_BASE_URL / ADMIN_PASSWORD_FILE / ADMIN_EMAIL 或 ADMIN_ENV_FILE（同 admin.js）
 *   MAIL_SUBJECT   缺省 perf bulk <时间戳>
 *   MAIL_BODY      缺省一段中文测试正文
 *   DRY_RUN        为 1 时只登录与查积压，不发 POST bulk/mail
 *   POLL_SEC       轮询间隔秒，缺省 5
 *   POLL_TIMEOUT_SEC  缺省 1800
 *
 * 阈值：checks 失败率 0。
 *
 * 示例：
 * K6_INSECURE_SKIP_TLS_VERIFY=true ADMIN_BASE_URL=https://panel.example.com/admin-xxxx \
 *   ADMIN_PASSWORD_FILE=/root/perf/admin.pass ADMIN_EMAIL=admin@example.test \
 *   k6 run bulk_mail.js
 */
import http from 'k6/http';
import { check, sleep } from 'k6';
import {
  env,
  requireEnv,
  num,
  adminEmail,
  readSecret,
  login,
  jsonHeaders,
  idemKey,
} from './lib.js';

const ADMIN_BASE = requireEnv('ADMIN_BASE_URL').replace(/\/$/, '');
const ADMIN_PASS = readSecret(requireEnv('ADMIN_PASSWORD_FILE'));
const ADMIN_MAIL = adminEmail();
const MAIL_SUBJECT = env('MAIL_SUBJECT', 'perf bulk ' + Date.now());
const MAIL_BODY = env(
  'MAIL_BODY',
  '这是一段用于性能测试的群发邮件正文，请勿回复。',
);
const DRY_RUN = env('DRY_RUN', '') === '1';
const POLL_SEC = num('POLL_SEC', 5);
const POLL_TIMEOUT_SEC = num('POLL_TIMEOUT_SEC', 1800);

let bearer = '';

function ensureToken() {
  if (bearer) {
    return bearer;
  }
  bearer = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
  return bearer;
}

function adminPost(path, body, tagName, extraHeaders) {
  const url = ADMIN_BASE + path;
  const opts = {
    headers: jsonHeaders(ensureToken(), extraHeaders),
    tags: { name: tagName },
  };
  let res = http.post(url, body, opts);
  if (res.status === 401) {
    bearer = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
    opts.headers = jsonHeaders(bearer, extraHeaders);
    res = http.post(url, body, opts);
  }
  return res;
}

function adminGet(path, tagName) {
  const url = ADMIN_BASE + path;
  const opts = {
    headers: jsonHeaders(ensureToken()),
    tags: { name: tagName },
  };
  let res = http.get(url, opts);
  if (res.status === 401) {
    bearer = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
    opts.headers = jsonHeaders(bearer);
    res = http.get(url, opts);
  }
  return res;
}

export const options = {
  setupTimeout: '2m',
  scenarios: {
    bulk: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '2h',
    },
  },
  thresholds: {
    checks: ['rate==1'],
  },
};

export function setup() {
  const token = login(ADMIN_BASE, ADMIN_MAIL, ADMIN_PASS, 'POST /admin/v1/auth/login');
  return { token };
}

export default function (data) {
  if (data && data.token) {
    bearer = data.token;
  }
  ensureToken();

  const reauthRes = adminPost(
    '/v1/auth/reauth',
    JSON.stringify({ password: ADMIN_PASS }),
    'POST /admin/v1/auth/reauth',
    null,
  );
  check(reauthRes, { 'reauth 200': (r) => r.status === 200 });
  if (reauthRes.status === 200) {
    const tok = reauthRes.json('access_token');
    if (tok) {
      bearer = tok;
    }
  }

  if (!DRY_RUN) {
    const mailBody = JSON.stringify({
      status: 'active',
      subject: MAIL_SUBJECT,
      body: MAIL_BODY,
    });
    const mailRes = adminPost(
      '/v1/users/bulk/mail',
      mailBody,
      'POST /admin/v1/users/bulk/mail',
      { 'Idempotency-Key': idemKey('perf-bulk') },
    );
    check(mailRes, { 'bulk mail 200': (r) => r.status === 200 });
    if (mailRes.status === 200) {
      const queued = mailRes.json('queued');
      const skipped = mailRes.json('skipped');
      console.log(
        'bulk mail queued=' +
          queued +
          ' skipped=' +
          skipped +
          ' at=' +
          new Date().toISOString(),
      );
    }
  } else {
    console.log('DRY_RUN=1，跳过 POST /v1/users/bulk/mail');
  }

  const t0 = Date.now();
  while (true) {
    const elapsed = (Date.now() - t0) / 1000;
    if (elapsed > POLL_TIMEOUT_SEC) {
      throw new Error('轮询积压超时 ' + POLL_TIMEOUT_SEC + 's');
    }
    const blRes = adminGet(
      '/v1/dashboard/backlog/notifications',
      'GET /admin/v1/dashboard/backlog/notifications',
    );
    check(blRes, { 'backlog 200': (r) => r.status === 200 });
    if (blRes.status !== 200) {
      sleep(POLL_SEC);
      continue;
    }
    const c = blRes.json('counts') || {};
    const ready = c.ready || 0;
    const readyRetry = c.ready_retry || 0;
    const scheduled = c.scheduled || 0;
    const scheduledRetry = c.scheduled_retry || 0;
    const failedTotal = c.failed_total || 0;
    const maxLag = blRes.json('max_ready_lag_seconds') || 0;
    const pending = ready + readyRetry + scheduled + scheduledRetry;
    console.log(
      't=' +
        Math.floor(elapsed) +
        ' ready=' +
        ready +
        ' ready_retry=' +
        readyRetry +
        ' scheduled=' +
        scheduled +
        ' scheduled_retry=' +
        scheduledRetry +
        ' failed_total=' +
        failedTotal +
        ' max_ready_lag_seconds=' +
        maxLag,
    );
    if (pending === 0) {
      break;
    }
    sleep(POLL_SEC);
  }
}
