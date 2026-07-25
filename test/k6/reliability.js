import http from 'k6/http';
import { check, group } from 'k6';
import { Rate } from 'k6/metrics';

import {
  baseURL,
  expected200,
  expected204,
  expected400,
  expected429,
  expected502,
  required,
  responseErrorCode,
  sendChat,
} from './common.js';

const gatewayURL = required('GATEWAY_URL');
const upstreamURL = required('UPSTREAM_URL');
const apiKey = required('API_KEY');
const reliabilitySuccess = new Rate('reliability_success');

export const options = {
  scenarios: {
    reliability: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '2m',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    reliability_success: ['rate==1'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

function record(response, assertions, tags) {
  const ok = check(response, assertions, tags);
  reliabilitySuccess.add(ok, tags);
}

function expectError(model, status, code, responseCallback) {
  const response = sendChat({
    targetURL: gatewayURL,
    apiKey,
    model,
    target: 'gateway',
    responseCallback,
  });
  record(
    response,
    {
      [`${model} maps to HTTP ${status}`]: (result) => result.status === status,
      [`${model} maps to ${code}`]: (result) => responseErrorCode(result) === code,
    },
    { model, expected_status: String(status) },
  );
}

export default function () {
  group('reset deterministic fake state', () => {
    const response = http.post(`${baseURL(upstreamURL)}/__admin/reset`, null, {
      responseCallback: expected204,
      tags: { target: 'upstream', operation: 'reset' },
    });
    record(
      response,
      {
        'fake upstream state reset': (result) => result.status === 204,
      },
      { operation: 'reset' },
    );
  });

  group('retry succeeds on third attempt', () => {
    const response = sendChat({
      targetURL: gatewayURL,
      apiKey,
      model: 'mock/retry-2',
      target: 'gateway',
      responseCallback: expected200,
    });
    record(
      response,
      {
        'retry sequence returns 200': (result) => result.status === 200,
        'retry sequence returns a completion': (result) =>
          String(result.body || '').includes('"object":"chat.completion"'),
      },
      { model: 'mock/retry-2' },
    );
  });

  group('fallback uses the healthy second provider', () => {
    const response = sendChat({
      targetURL: gatewayURL,
      apiKey,
      model: 'mock/fallback',
      target: 'gateway',
      responseCallback: expected200,
    });
    record(
      response,
      {
        'fallback sequence returns 200': (result) => result.status === 200,
        'fallback response came from fallback provider': (result) =>
          String(result.body || '').includes('mock response from fallback'),
      },
      { model: 'mock/fallback' },
    );
  });

  group('upstream HTTP errors are normalized', () => {
    expectError('mock/error-400', 400, 'upstream_rejected_request', expected400);
    expectError('mock/error-503', 502, 'upstream_http_error', expected502);
  });

  group('invalid first SSE event remains an HTTP error', () => {
    const response = sendChat({
      targetURL: gatewayURL,
      apiKey,
      model: 'mock/sse-error',
      stream: true,
      target: 'gateway',
      responseCallback: expected502,
    });
    record(
      response,
      {
        'invalid first event returns 502': (result) => result.status === 502,
        'invalid first event is a protocol error': (result) =>
          responseErrorCode(result) === 'upstream_protocol_error',
      },
      { model: 'mock/sse-error' },
    );
  });

  group('committed SSE stream never switches provider', () => {
    const response = sendChat({
      targetURL: gatewayURL,
      apiKey,
      model: 'mock/sse-drop',
      stream: true,
      target: 'gateway',
      responseCallback: expected200,
    });
    record(
      response,
      {
        'dropped stream was already committed': (result) => result.status === 200,
        'dropped stream contains the first chunk': (result) =>
          String(result.body || '').includes('token-01'),
        'dropped stream has no terminal marker': (result) =>
          !String(result.body || '').includes('data: [DONE]'),
      },
      { model: 'mock/sse-drop' },
    );
  });

  group('rate limit preserves Retry-After', () => {
    const response = sendChat({
      targetURL: gatewayURL,
      apiKey,
      model: 'mock/error-429',
      target: 'gateway',
      responseCallback: expected429,
    });
    record(
      response,
      {
        'upstream rate limit returns 429': (result) => result.status === 429,
        'upstream rate limit has the gateway error code': (result) =>
          responseErrorCode(result) === 'upstream_rate_limited',
        'upstream rate limit preserves Retry-After': (result) =>
          Number(result.headers['Retry-After']) >= 1,
      },
      { model: 'mock/error-429' },
    );
  });
}
