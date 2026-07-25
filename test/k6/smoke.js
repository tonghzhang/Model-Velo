import http from 'k6/http';
import { check } from 'k6';

import {
  baseURL,
  expected200,
  required,
  sendChat,
  verifySuccessfulChat,
} from './common.js';

const gatewayURL = required('GATEWAY_URL');
const upstreamURL = required('UPSTREAM_URL');
const apiKey = required('API_KEY');

http.setResponseCallback(expected200);

export const options = {
  scenarios: {
    smoke: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: '1m',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    chat_success: ['rate==1'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const upstreamHealth = http.get(`${baseURL(upstreamURL)}/healthz`, {
    tags: { target: 'upstream', operation: 'health' },
  });
  check(upstreamHealth, {
    'fake upstream is healthy': (response) => response.status === 200,
  });

  const gatewayReadiness = http.get(`${baseURL(gatewayURL)}/readyz`, {
    tags: { target: 'gateway', operation: 'readiness' },
  });
  check(gatewayReadiness, {
    'gateway is ready': (response) => response.status === 200,
  });

  const direct = sendChat({
    targetURL: upstreamURL,
    model: 'mock/instant',
    target: 'upstream',
  });
  verifySuccessfulChat(direct, false, { target: 'upstream', model: 'mock/instant' });

  const gateway = sendChat({
    targetURL: gatewayURL,
    apiKey,
    model: 'mock/instant',
    target: 'gateway',
  });
  verifySuccessfulChat(gateway, false, { target: 'gateway', model: 'mock/instant' });

  const stream = sendChat({
    targetURL: gatewayURL,
    apiKey,
    model: 'mock/typical',
    stream: true,
    target: 'gateway',
  });
  verifySuccessfulChat(stream, true, { target: 'gateway', model: 'mock/typical' });
}
