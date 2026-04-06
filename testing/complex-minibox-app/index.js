import express from "express";
import path from "path";
import { fileURLToPath } from "url";
import { config } from "./src/config.js";
import { createCache } from "./src/services/cache.js";
import { createWorker } from "./src/services/worker.js";
import { healthRouter } from "./src/routes/health.js";
import { apiRouter } from "./src/routes/api.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const app = express();
const cache = createCache(config.cacheTTLms);
const worker = createWorker(config.workerIntervalMs, cache);

app.use(express.json({ limit: "1mb" }));
app.use(express.urlencoded({ extended: true }));

app.use("/health", healthRouter({ worker, cache }));
app.use("/api", apiRouter({ cache, worker }));
app.use("/", express.static(path.join(__dirname, "public")));

app.get("/metrics", (_req, res) => {
  res.json({
    ok: true,
    uptimeSec: Math.floor(process.uptime()),
    now: new Date().toISOString(),
    memoryRss: process.memoryUsage().rss,
    workerTicks: worker.getTicks()
  });
});

worker.start();

const server = app.listen(config.port, () => {
  console.log(`Complex MiniBox app listening on ${config.port}`);
  console.log(`Mode=${config.nodeEnv} batching=${config.featureBatching}`);
});

function shutdown(signal) {
  console.log(`[shutdown] received ${signal}`);
  worker.stop();
  server.close(() => {
    console.log("[shutdown] http server closed");
    process.exit(0);
  });
}

process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
