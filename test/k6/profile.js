import {
  booleanValue,
  integerValue,
  required,
  sendChat,
  verifySuccessfulChat,
} from './common.js';

const targetURL = required('TARGET_URL');
const target = (__ENV.TARGET_NAME || 'gateway').trim();
const apiKey = (__ENV.API_KEY || '').trim();
const model = (__ENV.MODEL || 'mock/instant').trim();
const stream = booleanValue('STREAM', false);
const profileMode = (__ENV.PROFILE_MODE || 'rate').trim().toLowerCase();
const startValue = integerValue('START_VALUE', 1, 0);
const preAllocatedVUs = integerValue('PRE_ALLOCATED_VUS', 100);
const maxVUs = integerValue('MAX_VUS', preAllocatedVUs);
const promptBytes = integerValue('PROMPT_BYTES', 200, 64);
const cacheMode = (__ENV.CACHE_MODE || 'bypass').trim().toLowerCase();
const minimumSuccessRate = Number(__ENV.MIN_SUCCESS_RATE || '0');
const maximumFailureRate = Number(__ENV.MAX_FAILURE_RATE || '1');
const stages = parseStages(required('STAGES'));

if (!['rate', 'vus'].includes(profileMode)) {
  throw new Error('PROFILE_MODE must be rate or vus');
}
if (!['bypass', 'unique', 'shared'].includes(cacheMode)) {
  throw new Error('CACHE_MODE must be bypass, unique, or shared');
}
if (maxVUs < preAllocatedVUs) {
  throw new Error('MAX_VUS must be greater than or equal to PRE_ALLOCATED_VUS');
}
if (promptBytes > 512 * 1024) {
  throw new Error('PROMPT_BYTES must not exceed 524288');
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

function parseStages(value) {
  return value.split(',').map((entry) => {
    const separator = entry.indexOf(':');
    if (separator <= 0 || separator === entry.length - 1) {
      throw new Error('STAGES entries must use target:duration');
    }
    const targetValue = Number(entry.slice(0, separator));
    const duration = entry.slice(separator + 1).trim();
    if (!Number.isInteger(targetValue) || targetValue < 0 || !duration) {
      throw new Error('STAGES contains an invalid target or duration');
    }
    return { target: targetValue, duration };
  });
}

const scenario =
  profileMode === 'rate'
    ? {
        executor: 'ramping-arrival-rate',
        startRate: startValue,
        timeUnit: '1s',
        preAllocatedVUs,
        maxVUs,
        stages,
        gracefulStop: '30s',
      }
    : {
        executor: 'ramping-vus',
        startVUs: startValue,
        stages,
        gracefulRampDown: '30s',
      };

export const options = {
  scenarios: {
    profile: scenario,
  },
  thresholds: {
    checks: [`rate>=${minimumSuccessRate}`],
    chat_success: [`rate>=${minimumSuccessRate}`],
    http_req_failed: [`rate<=${maximumFailureRate}`],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  userAgent: 'model-velo-k6-profile',
};

export default function () {
  const response = sendChat({
    targetURL,
    apiKey,
    model,
    stream,
    target,
    promptBytes,
    cacheMode,
  });
  verifySuccessfulChat(response, stream, {
    target,
    model,
    stream: String(stream),
    profile_mode: profileMode,
  });
}
