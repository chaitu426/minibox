export function createCache(ttlMs) {
  const store = new Map();
  const ttl = Math.max(ttlMs, 1000);

  function set(key, value) {
    store.set(key, {
      value,
      expiresAt: Date.now() + ttl
    });
  }

  function get(key) {
    const found = store.get(key);
    if (!found) return null;
    if (Date.now() > found.expiresAt) {
      store.delete(key);
      return null;
    }
    return found.value;
  }

  function keys() {
    return [...store.keys()];
  }

  function size() {
    return keys().length;
  }

  return { set, get, keys, size };
}
