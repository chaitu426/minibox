import { Router } from "express";

export function apiRouter({ cache, worker }) {
  const router = Router();

  router.get("/config", (_req, res) => {
    res.json({
      ok: true,
      env: process.env.NODE_ENV || "unknown",
      port: process.env.PORT || "unset",
      workerTicks: worker.getTicks()
    });
  });

  router.post("/cache/:key", (req, res) => {
    const key = req.params.key;
    const value = req.body?.value;
    if (typeof value === "undefined") {
      return res.status(400).json({ ok: false, error: "missing body.value" });
    }
    cache.set(key, value);
    return res.json({ ok: true, key, size: cache.size() });
  });

  router.get("/cache/:key", (req, res) => {
    const key = req.params.key;
    const value = cache.get(key);
    if (value === null) {
      return res.status(404).json({ ok: false, error: "not found", key });
    }
    return res.json({ ok: true, key, value });
  });

  router.get("/cache", (_req, res) => {
    res.json({ ok: true, keys: cache.keys(), size: cache.size() });
  });

  return router;
}
