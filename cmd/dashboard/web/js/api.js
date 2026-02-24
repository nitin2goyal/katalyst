// API client module with request deduplication and caching
import { store } from './store.js';

const inflight = new Map();

export async function api(path) {
  const url = '/api/v1' + path;

  // Return cached response if fresh
  const cached = store.get('api:' + url);
  if (cached) return cached;

  // Deduplicate in-flight requests
  if (inflight.has(url)) return inflight.get(url);

  const promise = fetch(url).then(async res => {
    if (!res.ok) throw new Error(`API ${res.status}: ${res.statusText}`);
    const data = await res.json();
    store.set('api:' + url, data, 30000);
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
  const res = await fetch('/api/v1' + path, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`API ${res.status}: ${res.statusText}`);
  // Clear cache on mutations
  store.clear();
  return res.json();
}

export async function apiPut(path, body) {
  const res = await fetch('/api/v1' + path, {
    method: 'PUT',
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) throw new Error(`API ${res.status}: ${res.statusText}`);
  store.clear();
  return res.json();
}
