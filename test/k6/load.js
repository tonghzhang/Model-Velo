import {
  booleanValue,
  integerValue,
  required,
  sendChat,
  verifySuccessfulChat,
} from './common.js';

const targetURL = required('TARGET_URL');
const target = (__ENV.TARGET_NAME || 'target').trim();
const apiKey = (__ENV.API_KEY || '').trim();
const model = (__ENV.MODEL || 'mock/instant').trim();
const stream = booleanValue('STREAM', false);
const mode = (__ENV.LOAD_MODE || 'rate').trim().toLowerCase();
const duration = (__ENV.DURATION || '30s').trim();
const rate = integerValue('RATE', 20);
const vus = integerValue('VUS', 10);
const preAllocatedVUs = integerValue('PRE_ALLOCATED_VUS', Math.max(rate, 10));
const maxVUs = integerValue('MAX_VUS', preAllocatedVUs);
const minimumSuccessRate = Number(__ENV.MIN_SUCCESS_RATE || '0.99');
const maximumFailureRate = Number(__ENV.MAX_FAILURE_RATE || '0.01');

if (!model) {
  throw new Error('MODEL must not be empty');
}
if (!['rate', 'vus'].includes(mode)) {
  throw new Error('LOAD_MODE must be rate or vus');
}
if (maxVUs < preAllocatedVUs) {
  throw new Error('MAX_VUS must be greater than or equal to PRE_ALLOCATED_VUS');
}
if (
  !Number.isFinite(minimumSuccessRate) ||
  minimumSuccessRate < 0 ||
  minimumSuccessRate > 1
) {
  throw new Error('MIN_SUCCESS_RATE must be between 0 and 1');
}
if (
  !Number.isFinite(maximumFailureRate) ||
  maximumFailureRate < 0 ||
  maximumFailureRate > 1
) {
  throw new Error('MAX_FAILURE_RATE must be between 0 and 1');
}

const scenario =
  mode === 'rate'
    ? {
        executor: 'constant-arrival-rate',
        rate,
        timeUnit: '1s',
        duration,
        preAllocatedVUs,
        maxVUs,
        gracefulStop: '30s',
      }
    : {
        executor: 'constant-vus',
        vus,
        duration,
        gracefulStop: '30s',
      };

export const options = {
  scenarios: {
    load: scenario,
  },
  thresholds: {
    checks: [`rate>=${minimumSuccessRate}`],
    chat_success: [`rate>=${minimumSuccessRate}`],
    http_req_failed: [`rate<=${maximumFailureRate}`],
    ...(mode === 'rate' ? { dropped_iterations: ['count==0'] } : {}),
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  userAgent: 'model-velo-k6',
};

export default function () {
  const response = sendChat({
    targetURL,
    apiKey,
    model,
    stream,
    target,
  });
  verifySuccessfulChat(response, stream, { target, model, stream: String(stream) });
}
