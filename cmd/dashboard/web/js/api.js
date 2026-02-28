// API client module with request deduplication, caching, and retry
import { store } from './store.js';

const inflight = new Map();
const MAX_RETRIES = 3;
const RETRY_DELAY = 1000; // 1 second base delay

async function fetchWithRetry(url, opts, retries = MAX_RETRIES) {
  for (let attempt = 1; attempt <= retries; attempt++) {
    try {
      const res = await fetch(url, opts);
      if (!res.ok) {
        // Retry on server errors (5xx), not client errors (4xx)
        if (res.status >= 500 && attempt < retries) {
          await new Promise(r => setTimeout(r, RETRY_DELAY * attempt));
          continue;
        }
        throw new Error(`API ${res.status}: ${res.statusText}`);
      }
      return res;
    } catch (err) {
      if (attempt < retries && !err.message.startsWith('API 4')) {
        await new Promise(r => setTimeout(r, RETRY_DELAY * attempt));
        continue;
      }
      throw err;
    }
  }
}

export async function api(path) {
  const url = '/api/v1' + path;

  // Return cached response if fresh
  const cached = store.get('api:' + url);
  if (cached) return cached;

  // Deduplicate in-flight requests
  if (inflight.has(url)) return inflight.get(url);

  const promise = fetchWithRetry(url).then(async res => {
    const data = await res.json();
    store.set('api:' + url, data, 300000); // 5 min cache
    inflight.delete(url);
    return data;
  }).catch(err => {
    inflight.delete(url);
    throw err;
  });

  inflight.set(url, promise);
  return promise;
}

export async function apiPost(path, body) {
  const res = await fetchWithRetry('/api/v1' + path, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  // Clear cache on mutations
  store.clear();
  return res.json();
}

export async function apiPut(path, body) {
  const res = await fetchWithRetry('/api/v1' + path, {
    method: 'PUT',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  store.clear();
  return res.json();
}

export async function apiDelete(path) {
  const res = await fetchWithRetry('/api/v1' + path, { method: 'DELETE' });
  store.clear();
  return res.json();
}
