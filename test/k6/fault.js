import http from 'k6/http';
import { check } from 'k6';
import { Rate } from 'k6/metrics';

import {
  booleanValue,
  integerValue,
  recordHTTPStatus,
  required,
  sendChat,
} from './common.js';

const targetURL = required('TARGET_URL');
const target = (__ENV.TARGET_NAME || 'gateway').trim();
const apiKey = (__ENV.API_KEY || '').trim();
const model = required('MODEL');
const stream = booleanValue('STREAM', false);
const duration = (__ENV.DURATION || '60s').trim();
const rate = integerValue('RATE', 100);
const preAllocatedVUs = integerValue('PRE_ALLOCATED_VUS', Math.max(rate, 100));
const maxVUs = integerValue('MAX_VUS', preAllocatedVUs);
const promptBytes = integerValue('PROMPT_BYTES', 200, 64);
const allowedStatuses = parseStatuses(__ENV.ALLOWED_STATUSES || '200,429,502,503');
const minimumExpectedRate = Number(__ENV.MIN_EXPECTED_RATE || '0.99');
const expectedResponse = http.expectedStatuses(...allowedStatuses);
const expectedOutcome = new Rate('fault_expected_outcome');

if (maxVUs < preAllocatedVUs) {
  throw new Error('MAX_VUS must be greater than or equal to PRE_ALLOCATED_VUS');
}
if (promptBytes > 512 * 1024) {
  throw new Error('PROMPT_BYTES must not exceed 524288');
}
if (
  !Number.isFinite(minimumExpectedRate) ||
  minimumExpectedRate < 0 ||
  minimumExpectedRate > 1
) {
  throw new Error('MIN_EXPECTED_RATE must be between 0 and 1');
}

function parseStatuses(value) {
  const statuses = value.split(',').map((entry) => Number(entry.trim()));
  if (
    statuses.length === 0 ||
    statuses.some((status) => !Number.isInteger(status) || status < 100 || status > 599)
  ) {
    throw new Error('ALLOWED_STATUSES must contain comma-separated HTTP statuses');
  }
  return statuses;
}

function validBody(response) {
  try {
    const payload = response.json();
    if (response.status === 200) {
      return payload.object === 'chat.completion' && payload.choices?.[0]?.message;
    }
    return Boolean(payload?.error?.code);
  } catch (_) {
    return false;
  }
}

export const options = {
  scenarios: {
    fault: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs,
      maxVUs,
      gracefulStop: '30s',
    },
  },
  thresholds: {
    checks: [`rate>=${minimumExpectedRate}`],
    fault_expected_outcome: [`rate>=${minimumExpectedRate}`],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  userAgent: 'model-velo-k6-fault',
};

export default function () {
  const response = sendChat({
    targetURL,
    apiKey,
    model,
    stream,
    target,
    responseCallback: expectedResponse,
    promptBytes,
    cacheMode: 'bypass',
  });
  const tags = { target, model, stream: String(stream) };
  recordHTTPStatus(response, tags);
  const ok = check(
    response,
    {
      'fault response status is expected': (result) => allowedStatuses.includes(result.status),
      'fault response body matches its status': validBody,
    },
    tags,
  );
  expectedOutcome.add(ok, tags);
}
