function getNumber(name, fallback) {
  const raw = process.env[name];
  if (!raw) return fallback;
  const n = Number(raw);
  return Number.isFinite(n) ? n : fallback;
}

function getBool(name, fallback) {
  const raw = process.env[name];
  if (!raw) return fallback;
  const v = raw.trim().toLowerCase();
  return v === "1" || v === "true" || v === "yes";
}

export const config = {
  port: getNumber("PORT", 3000),
  nodeEnv: process.env.NODE_ENV || "development",
  cacheTTLms: getNumber("CACHE_TTL_MS", 5000),
  workerIntervalMs: getNumber("WORKER_INTERVAL_MS", 3000),
  featureBatching: getBool("FEATURE_BATCHING", false)
};
