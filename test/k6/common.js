import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

export const chatSuccess = new Rate('chat_success');
export const streamFirstByte = new Trend('stream_first_byte_ms', true);
export const responses200 = new Counter('chat_responses_200');
export const responses429 = new Counter('chat_responses_429');
export const responses5xx = new Counter('chat_responses_5xx');
export const responsesOther = new Counter('chat_responses_other');
export const requestTimeout = __ENV.REQUEST_TIMEOUT || '20s';

export const expected200 = http.expectedStatuses(200);
export const expected204 = http.expectedStatuses(204);
export const expected400 = http.expectedStatuses(400);
export const expected429 = http.expectedStatuses(429);
export const expected502 = http.expectedStatuses(502);

export function required(name) {
  const value = (__ENV[name] || '').trim();
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

export function booleanValue(name, fallback) {
  const raw = (__ENV[name] || '').trim().toLowerCase();
  if (!raw) {
    return fallback;
  }
  if (raw === 'true') {
    return true;
  }
  if (raw === 'false') {
    return false;
  }
  throw new Error(`${name} must be true or false`);
}

export function integerValue(name, fallback, minimum = 1) {
  const raw = (__ENV[name] || '').trim();
  if (!raw) {
    return fallback;
  }
  const value = Number(raw);
  if (!Number.isInteger(value) || value < minimum) {
    throw new Error(`${name} must be an integer greater than or equal to ${minimum}`);
  }
  return value;
}

export function baseURL(value) {
  return value.replace(/\/+$/, '');
}

export function chatURL(value) {
  return `${baseURL(value)}/v1/chat/completions`;
}

export function requestID(target) {
  const prefix = (__ENV.REQUEST_PREFIX || 'k6').trim();
  const safePrefix = prefix.replace(/[^A-Za-z0-9._-]/g, '-').slice(0, 48);
  const safeTarget = target.replace(/[^A-Za-z0-9_-]/g, '-').slice(0, 16);
  const random = Math.floor(Math.random() * 0x100000000).toString(16);
  return `${safePrefix}-${safeTarget}-${__VU}-${__ITER}-${Date.now().toString(36)}-${random}`;
}

function promptContent(id, promptBytes, cacheMode) {
  const identity = cacheMode === 'shared' ? 'shared' : id;
  let content = `model-velo benchmark request ${identity} `;
  if (content.length < promptBytes) {
    content += 'x'.repeat(promptBytes - content.length);
  }
  return content.slice(0, promptBytes);
}

export function recordHTTPStatus(response, tags = {}) {
  switch (response.status) {
    case 200:
      responses200.add(1, tags);
      break;
    case 429:
      responses429.add(1, tags);
      break;
    default:
      if (response.status >= 500 && response.status <= 599) {
        responses5xx.add(1, tags);
      } else {
        responsesOther.add(1, tags);
      }
  }
}

export function sendChat({
  targetURL,
  apiKey = '',
  model,
  stream = false,
  target = 'unknown',
  responseCallback = expected200,
  promptBytes = 200,
  cacheMode = 'bypass',
}) {
  const id = requestID(target);
  const headers = {
    'Content-Type': 'application/json',
    'X-Request-ID': id,
  };
  if (cacheMode === 'bypass') {
    headers['Cache-Control'] = 'no-store';
  }
  if (apiKey) {
    headers.Authorization = `Bearer ${apiKey}`;
  }

  const body = JSON.stringify({
    model,
    messages: [
      {
        role: 'user',
        content: promptContent(id, promptBytes, cacheMode),
      },
    ],
    stream,
  });

  return http.post(chatURL(targetURL), body, {
    headers,
    responseCallback,
    tags: {
      model,
      stream: String(stream),
      target,
      cache_mode: cacheMode,
      prompt_bytes: String(promptBytes),
    },
    timeout: requestTimeout,
  });
}

export function verifySuccessfulChat(response, stream, tags = {}) {
  recordHTTPStatus(response, tags);
  const assertions = stream
    ? {
        'stream status is 200': (res) => res.status === 200,
        'stream content type is SSE': (res) =>
          String(res.headers['Content-Type'] || '').toLowerCase().includes('text/event-stream'),
        'stream has terminal marker': (res) => String(res.body || '').includes('data: [DONE]'),
      }
    : {
        'chat status is 200': (res) => res.status === 200,
        'chat body is a completion': (res) => {
          try {
            const payload = res.json();
            return (
              payload.object === 'chat.completion' &&
              payload.choices &&
              payload.choices[0] &&
              payload.choices[0].message &&
              payload.choices[0].message.role === 'assistant'
            );
          } catch (_) {
            return false;
          }
        },
      };

  const ok = check(response, assertions, tags);
  chatSuccess.add(ok, tags);
  if (stream && response.status === 200) {
    // Model-Velo only commits response headers with the first validated SSE event,
    // so waiting time is a TTFT proxy for this test topology.
    streamFirstByte.add(response.timings.waiting, tags);
  }
  return ok;
}

export function responseErrorCode(response) {
  try {
    const payload = response.json();
    return payload && payload.error && payload.error.code;
  } catch (_) {
    return '';
  }
}
