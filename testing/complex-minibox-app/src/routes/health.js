import { Router } from "express";

export function healthRouter({ worker, cache }) {
  const router = Router();

  router.get("/", (_req, res) => {
    const lastTick = cache.get("lastTick");
    res.json({
      ok: true,
      workerRunning: worker.isRunning(),
      workerTicks: worker.getTicks(),
      lastTick
    });
  });

  return router;
}
