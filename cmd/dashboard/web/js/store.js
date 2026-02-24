// Simple TTL cache for API responses
const cache = new Map();

export const store = {
  get(key) {
    const entry = cache.get(key);
    if (!entry) return null;
    if (Date.now() > entry.expires) {
      cache.delete(key);
      return null;
    }
    return entry.value;
  },

  set(key, value, ttlMs = 30000) {
    cache.set(key, { value, expires: Date.now() + ttlMs });
  },

  clear() {
    cache.clear();
  },
};
