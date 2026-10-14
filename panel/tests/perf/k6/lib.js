/**
 * k6 压测共用：环境变量、用户 CSV、登录、易支付签名。
 * readSecret / loadUsers 里的 open() 只能在 init 阶段调用。
 */
import http from 'k6/http';
import { sleep } from 'k6';
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import crypto from 'k6/crypto';

export const TREND_STATS = ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'];

export function env(name, def) {
  const v = __ENV[name];
  if (v === undefined || v === '') {
    return def;
  }
  return v;
}

export function requireEnv(name) {
  const v = __ENV[name];
  if (v === undefined || v === '') {
    throw new Error('缺环境变量 ' + name);
  }
  return v;
}

export function num(name, def) {
  const raw = env(name, def === undefined ? '' : String(def));
  if (raw === '' && def !== undefined) {
    return def;
  }
  const n = parseFloat(raw);
  if (Number.isNaN(n)) {
    throw new Error('环境变量 ' + name + ' 不是合法数字: ' + raw);
  }
  return n;
}

/** 只能在 init 阶段调用 */
export function readSecret(path) {
  const s = open(path).trim();
  if (!s) {
    throw new Error('密钥文件为空: ' + path);
  }
  return s;
}

export function loadUsers(path) {
  return new SharedArray('users', () => {
    const text = open(path);
    const lines = text.split(/\r?\n/);
    const out = [];
    let headerChecked = false;
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) {
        continue;
      }
      if (!headerChecked) {
        if (trimmed !== 'user_id,email,password,sub_token') {
          throw new Error('users.csv 表头必须是 user_id,email,password,sub_token');
        }
        headerChecked = true;
        continue;
      }
      const parts = trimmed.split(',');
      if (parts.length !== 4) {
        throw new Error('users.csv 行字段数不是 4: ' + trimmed.slice(0, 80));
      }
      out.push({
        user_id: parts[0],
        email: parts[1],
        password: parts[2],
        sub_token: parts[3],
      });
    }
    if (!headerChecked) {
      throw new Error('users.csv 为空或缺少表头');
    }
    return out;
  });
}

export function adminEmail() {
  const direct = __ENV.ADMIN_EMAIL;
  if (direct !== undefined && direct !== '') {
    return direct;
  }
  const file = __ENV.ADMIN_ENV_FILE;
  if (!file) {
    throw new Error('需要 ADMIN_EMAIL 或 ADMIN_ENV_FILE');
  }
  const lines = open(file).split(/\r?\n/);
  for (const line of lines) {
    const t = line.trim();
    if (t.startsWith('ADMIN_EMAIL=')) {
      const v = t.slice('ADMIN_EMAIL='.length).trim();
      if (v) {
        return v;
      }
    }
  }
  throw new Error('ADMIN_ENV_FILE 中未找到 ADMIN_EMAIL=');
}

export function jsonHeaders(token, extra) {
  const h = { 'Content-Type': 'application/json' };
  if (token) {
    h.Authorization = 'Bearer ' + token;
  }
  if (extra) {
    for (const k of Object.keys(extra)) {
      h[k] = extra[k];
    }
  }
  return h;
}

export function idemKey(prefix) {
  const vu = exec.vu;
  return (
    prefix +
    '-' +
    vu.idInTest +
    '-' +
    vu.iterationInScenario +
    '-' +
    Date.now() +
    '-' +
    Math.floor(Math.random() * 1e9)
  );
}

function reqIdFromResponse(res) {
  try {
    const j = res.json();
    if (j && j.req_id) {
      return String(j.req_id);
    }
  } catch (_) {
    /* 非 JSON */
  }
  const h = res.headers['X-Request-Id'] || res.headers['X-Request-ID'];
  if (h) {
    return String(h);
  }
  return '';
}

export function login(base, email, password, tagName) {
  const url = base.replace(/\/$/, '') + '/v1/auth/login';
  const body = JSON.stringify({ email, password });
  const maxAttempts = 6;
  let lastRes = null;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    lastRes = http.post(url, body, {
      headers: { 'Content-Type': 'application/json' },
      tags: { name: tagName },
    });
    if (lastRes.status === 200) {
      const tok = lastRes.json('access_token');
      if (!tok) {
        throw new Error('登录 200 但无 access_token req_id=' + reqIdFromResponse(lastRes));
      }
      return tok;
    }
    if (lastRes.status === 429 && attempt < maxAttempts - 1) {
      const ra = lastRes.headers['Retry-After'] || lastRes.headers['retry-after'];
      let sec = 5;
      if (ra) {
        const p = parseFloat(ra);
        if (!Number.isNaN(p) && p > 0) {
          sec = p;
        }
      }
      sleep(sec);
      continue;
    }
    break;
  }
  const rid = reqIdFromResponse(lastRes);
  throw new Error('登录失败 HTTP ' + lastRes.status + (rid ? ' req_id=' + rid : ''));
}

export function parseQuery(url) {
  const i = url.indexOf('?');
  if (i < 0) {
    return {};
  }
  const q = url.slice(i + 1);
  const out = {};
  if (!q) {
    return out;
  }
  const pairs = q.split('&');
  for (const pair of pairs) {
    if (!pair) {
      continue;
    }
    const eq = pair.indexOf('=');
    if (eq < 0) {
      out[decodeURIComponent(pair.replace(/\+/g, ' '))] = '';
      continue;
    }
    const k = pair.slice(0, eq);
    const v = pair.slice(eq + 1);
    out[decodeURIComponent(k.replace(/\+/g, ' '))] = decodeURIComponent(v.replace(/\+/g, ' '));
  }
  return out;
}

/**
 * 与 panel/tests/epay_e2e.sh 的 epay_sign 一致：非空值、排除 sign/sign_type、键名升序 k=v&、末尾直接接密钥。
 */
export function epaySign(params, key) {
  const keys = Object.keys(params)
    .filter((k) => k !== 'sign' && k !== 'sign_type')
    .filter((k) => {
      const v = params[k];
      return v !== undefined && v !== null && String(v) !== '';
    })
    .sort();
  const parts = [];
  for (const k of keys) {
    parts.push(k + '=' + params[k]);
  }
  const s = parts.join('&') + key;
  return crypto.md5(s, 'hex');
}
